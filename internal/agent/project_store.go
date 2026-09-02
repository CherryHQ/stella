package agent

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/platform/home"
	"github.com/CherryHQ/stella/internal/skill"
	sqlc "github.com/CherryHQ/stella/pkg/db/sqlc"
)

// ProjectAgentAuthorizer is the narrow Agent PEP port the project use cases gate
// on: a project belongs to an agent, so every use case first authorizes the
// caller's read access to that agent. agentaccess.Service satisfies it.
type ProjectAgentAuthorizer interface {
	Authorize(ctx context.Context, authority authz.Authority, agentID string, action authz.Action) error
}

// ProjectStore owns the project application use cases (Authority-bound
// list/create/get/update/delete) plus the runtime workspace resolution the pool
// manager binds: Resolve satisfies ProjectResolverFunc. A project is owned by
// (user, agent); the CRUD use cases
// enforce that ownership, the route-agent binding, and the workspace-containment
// invariant, and return the transport-neutral Project value.
type ProjectStore struct {
	q      *sqlc.Queries
	agents ProjectAgentAuthorizer
	homes  home.Workspace
}

// SnapshotSkills resolves exact project ownership and snapshots through Home.
func (ps *ProjectStore) SnapshotSkills(ctx context.Context, projectID, userID, agentID string) (*skill.ProjectSnapshot, ProjectDescriptor, error) {
	return SnapshotAuthorizedProjectSkills(ctx, ps.Resolve, ps.homes, projectID, userID, agentID)
}

// NewProjectStore builds a ProjectStore over the given pool and config store.
// agents is the Agent PEP the CRUD use cases gate on; it may be nil for the
// runtime-only Resolve/Ensure paths (which perform no authorization).
type ProjectStoreOption func(*ProjectStore)

func WithProjectHomeWorkspace(viewer home.Workspace) ProjectStoreOption {
	return func(s *ProjectStore) { s.homes = viewer }
}

func NewProjectStore(db *pgxpool.Pool, agents ProjectAgentAuthorizer, opts ...ProjectStoreOption) *ProjectStore {
	s := &ProjectStore{q: sqlc.New(db), agents: agents}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Project is the transport-neutral view of a project row. Description is a plain
// string ("" when unset) and timestamps are UTC.
type Project struct {
	ID            string
	AgentID       string
	UserID        string
	Name          string
	BaseDir       string
	Description   string
	Archived      bool
	IsUnavailable bool
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Project use-case errors. Agent-gate denials propagate the agentaccess
// sentinels unchanged.
var (
	// ErrProjectNotFound is the opaque "not found" for a missing project, a
	// project owned by another user, or one bound to a different route agent (a
	// route-agent mismatch never confirms the project exists).
	ErrProjectNotFound = errors.New("project not found")
	// ErrInvalidBaseDir reports a base_dir that escapes the agent workspace (400).
	ErrInvalidBaseDir = errors.New("invalid base_dir")
)

// ProjectUpdate carries the optional fields of an update; a nil field leaves the
// stored value unchanged.
type ProjectUpdate struct {
	Name        *string
	BaseDir     *string
	Description *string
}

// gate authorizes the caller's read access to the agent that owns the project.
func (s *ProjectStore) gate(ctx context.Context, authority authz.Authority, agentID string) error {
	return s.agents.Authorize(ctx, authority, agentID, authz.ActionRead)
}

// List returns the agent's projects owned by the caller, optionally including
// archived ones. Rows waiting for coordinate repair remain visible but cannot
// expose their historical host path. Gated on agent read access.
func (s *ProjectStore) List(ctx context.Context, authority authz.Authority, agentID string, includeArchived bool) ([]Project, error) {
	if err := s.gate(ctx, authority, agentID); err != nil {
		return nil, err
	}
	userID := string(authority.UserID())
	var rows []sqlc.Project
	var err error
	if includeArchived {
		rows, err = s.q.ListProjectsAll(ctx, sqlc.ListProjectsAllParams{AgentID: agentID, UserID: userID})
	} else {
		rows, err = s.q.ListProjects(ctx, sqlc.ListProjectsParams{AgentID: agentID, UserID: userID})
	}
	if err != nil {
		return nil, err
	}
	out := make([]Project, 0, len(rows))
	for _, p := range rows {
		project, err := s.projectFromRow(ctx, p)
		if errors.Is(err, ErrInvalidBaseDir) {
			out = append(out, unavailableProjectFromRow(p))
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, project)
	}
	return out, nil
}

// Create persists a new project for the caller under the agent, after validating
// that base_dir is contained in the agent workspace. Gated on agent read access.
func (s *ProjectStore) Create(ctx context.Context, authority authz.Authority, agentID, name, baseDir string, description *string) (Project, error) {
	if err := s.gate(ctx, authority, agentID); err != nil {
		return Project{}, err
	}
	userID := string(authority.UserID())
	baseDir, err := projectCoordinate(baseDir)
	if err != nil {
		return Project{}, err
	}
	p, err := s.q.CreateProject(ctx, sqlc.CreateProjectParams{
		ID:          uuid.Must(uuid.NewV7()).String(),
		AgentID:     agentID,
		UserID:      userID,
		Name:        name,
		BaseDir:     baseDir,
		Description: pgtype.Text{String: derefString(description), Valid: description != nil},
	})
	if err != nil {
		return Project{}, err
	}
	return s.projectFromRow(ctx, p)
}

// Get returns one owned project bound to the route agent. A row waiting for
// coordinate repair is returned unavailable so callers can repair or delete it.
// A missing project, foreign owner, or route-agent mismatch returns not found.
func (s *ProjectStore) Get(ctx context.Context, authority authz.Authority, agentID, projectID string) (Project, error) {
	if err := s.gate(ctx, authority, agentID); err != nil {
		return Project{}, err
	}
	p, err := s.getOwned(ctx, string(authority.UserID()), agentID, projectID)
	if err != nil {
		return Project{}, err
	}
	project, err := s.projectFromRow(ctx, p)
	if errors.Is(err, ErrInvalidBaseDir) {
		return unavailableProjectFromRow(p), nil
	}
	return project, err
}

// Update merges the provided fields into an owned project bound to the route
// agent and persists it. A supplied base_dir is re-validated for containment.
func (s *ProjectStore) Update(ctx context.Context, authority authz.Authority, agentID, projectID string, in ProjectUpdate) (Project, error) {
	if err := s.gate(ctx, authority, agentID); err != nil {
		return Project{}, err
	}
	userID := string(authority.UserID())
	existing, err := s.getOwned(ctx, userID, agentID, projectID)
	if err != nil {
		return Project{}, err
	}
	name := existing.Name
	if in.Name != nil {
		name = *in.Name
	}
	baseDirValue := existing.BaseDir
	if in.BaseDir != nil {
		baseDirValue = *in.BaseDir
	}
	baseDir, err := projectCoordinate(baseDirValue)
	if err != nil {
		return Project{}, err
	}
	description := existing.Description
	if in.Description != nil {
		description = pgtype.Text{String: *in.Description, Valid: true}
	}
	updated, err := s.q.UpdateProject(ctx, sqlc.UpdateProjectParams{
		Name:        name,
		Description: description,
		BaseDir:     baseDir,
		ID:          projectID,
		UserID:      userID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Project{}, ErrProjectNotFound
	}
	if err != nil {
		return Project{}, err
	}
	return s.projectFromRow(ctx, updated)
}

// Delete removes an owned project bound to the route agent.
func (s *ProjectStore) Delete(ctx context.Context, authority authz.Authority, agentID, projectID string) error {
	if err := s.gate(ctx, authority, agentID); err != nil {
		return err
	}
	userID := string(authority.UserID())
	if _, err := s.getOwned(ctx, userID, agentID, projectID); err != nil {
		return err
	}
	return s.q.DeleteProject(ctx, sqlc.DeleteProjectParams{ID: projectID, UserID: userID})
}

// getOwned loads a project by id for the user and enforces the route-agent
// binding, collapsing all "cannot see it" cases to ErrProjectNotFound.
func (s *ProjectStore) getOwned(ctx context.Context, userID, agentID, projectID string) (sqlc.Project, error) {
	p, err := s.q.GetProject(ctx, sqlc.GetProjectParams{ID: projectID, UserID: userID})
	if isProjectNotFound(err) {
		return sqlc.Project{}, ErrProjectNotFound
	}
	if err != nil {
		return sqlc.Project{}, err
	}
	if p.AgentID != agentID {
		return sqlc.Project{}, ErrProjectNotFound
	}
	return p, nil
}

func (s *ProjectStore) projectFromRow(ctx context.Context, p sqlc.Project) (Project, error) {
	baseDir, err := projectCoordinate(p.BaseDir)
	if err != nil {
		return Project{}, err
	}
	description := ""
	if p.Description.Valid {
		description = p.Description.String
	}
	return Project{
		ID:          p.ID,
		AgentID:     p.AgentID,
		UserID:      p.UserID,
		Name:        p.Name,
		BaseDir:     baseDir,
		Description: description,
		Archived:    p.Archived,
		CreatedAt:   p.CreatedAt.UTC(),
		UpdatedAt:   p.UpdatedAt.UTC(),
	}, nil
}

func unavailableProjectFromRow(p sqlc.Project) Project {
	description := ""
	if p.Description.Valid {
		description = p.Description.String
	}
	return Project{
		ID: p.ID, AgentID: p.AgentID, UserID: p.UserID, Name: p.Name,
		Description: description, Archived: p.Archived, IsUnavailable: true,
		CreatedAt: p.CreatedAt.UTC(), UpdatedAt: p.UpdatedAt.UTC(),
	}
}

func projectCoordinate(value string) (string, error) {
	if value == "" {
		value = "."
	}
	scope, name, err := home.ResolveLogicalCoordinate(home.RootAgentWorkspace, value, true)
	if err != nil || scope != home.RootAgentWorkspace {
		return "", ErrInvalidBaseDir
	}
	return name, nil
}

// isProjectNotFound mirrors the transport's not-found predicate: an empty result
// or an id that is not a valid uuid (SQLSTATE 22P02) both mean "does not exist".
func isProjectNotFound(err error) bool {
	if errors.Is(err, pgx.ErrNoRows) {
		return true
	}
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "22P02"
}

func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// Resolve returns the logical descriptor for the exact project owner tuple.
func (s *ProjectStore) Resolve(ctx context.Context, projectID, userID, agentID string) (ProjectDescriptor, error) {
	p, err := s.q.GetProjectByOwner(ctx, sqlc.GetProjectByOwnerParams{ID: projectID, UserID: userID, AgentID: agentID})
	if err != nil {
		return ProjectDescriptor{}, err
	}
	relative, err := projectCoordinate(p.BaseDir)
	if err != nil {
		return ProjectDescriptor{}, err
	}
	return ProjectDescriptor{ID: p.ID, UserID: p.UserID, AgentID: p.AgentID, Path: relative}, nil
}
