package agent

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/asset"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/home"
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
// manager binds: Resolve satisfies ProjectResolverFunc and Ensure satisfies
// ProjectEnsurerFunc. A project is owned by (user, agent); the CRUD use cases
// enforce that ownership, the route-agent binding, and the workspace-containment
// invariant, and return the transport-neutral Project value.
type ProjectStore struct {
	q      *sqlc.Queries
	store  config.Store
	assets *asset.Store
	agents ProjectAgentAuthorizer
	homes  home.WorkspaceViewer
}

// NewProjectStore builds a ProjectStore over the given pool and config store.
// assets is the authoritative asset store used to hydrate a cold pod's asset
// tree when a project is first created; it may be nil (hydration is skipped).
// agents is the Agent PEP the CRUD use cases gate on; it may be nil for the
// runtime-only Resolve/Ensure paths (which perform no authorization).
type ProjectStoreOption func(*ProjectStore)

func WithProjectHomeWorkspace(viewer home.WorkspaceViewer) ProjectStoreOption {
	return func(s *ProjectStore) { s.homes = viewer }
}

func NewProjectStore(db *pgxpool.Pool, store config.Store, assets *asset.Store, agents ProjectAgentAuthorizer, opts ...ProjectStoreOption) *ProjectStore {
	s := &ProjectStore{q: sqlc.New(db), store: store, assets: assets, agents: agents}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Project is the transport-neutral view of a project row. Description is a plain
// string ("" when unset) and timestamps are UTC.
type Project struct {
	ID          string
	AgentID     string
	UserID      string
	Name        string
	BaseDir     string
	Description string
	Archived    bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
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
	// ErrWorkspaceSetup reports a failure to resolve/create the agent workspace (500).
	ErrWorkspaceSetup = errors.New("failed to resolve workspace")
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
// archived ones. Gated on agent read access.
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
		out = append(out, projectFromRow(p))
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
	if err := s.validateBaseDir(ctx, userID, agentID, baseDir); err != nil {
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
	return projectFromRow(p), nil
}

// Get returns one owned project bound to the route agent. A missing project, a
// foreign owner, or a route-agent mismatch all return ErrProjectNotFound.
func (s *ProjectStore) Get(ctx context.Context, authority authz.Authority, agentID, projectID string) (Project, error) {
	if err := s.gate(ctx, authority, agentID); err != nil {
		return Project{}, err
	}
	p, err := s.getOwned(ctx, string(authority.UserID()), agentID, projectID)
	if err != nil {
		return Project{}, err
	}
	return projectFromRow(p), nil
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
	baseDir := existing.BaseDir
	if in.BaseDir != nil {
		if err := s.validateBaseDir(ctx, userID, agentID, *in.BaseDir); err != nil {
			return Project{}, err
		}
		baseDir = *in.BaseDir
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
	return projectFromRow(updated), nil
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

// validateBaseDir ensures the agent workspace exists and base_dir is contained in
// it (a project is owned by the agent, #442, so it must live under the agent's
// subdir of the user home). It resolves the process-global home once.
func (s *ProjectStore) validateBaseDir(ctx context.Context, userID, agentID, baseDir string) error {
	if s.homes == nil {
		return ErrWorkspaceSetup
	}
	view, err := s.homes.WorkspaceView(ctx, home.WorkspaceRequest{UserID: userID, AgentID: agentID})
	if err != nil {
		return ErrWorkspaceSetup
	}
	if err := ValidateProjectDir(baseDir, view.AgentRoot); err != nil {
		return ErrInvalidBaseDir
	}
	return nil
}

func projectFromRow(p sqlc.Project) Project {
	description := ""
	if p.Description.Valid {
		description = p.Description.String
	}
	return Project{
		ID:          p.ID,
		AgentID:     p.AgentID,
		UserID:      p.UserID,
		Name:        p.Name,
		BaseDir:     p.BaseDir,
		Description: description,
		Archived:    p.Archived,
		CreatedAt:   p.CreatedAt.UTC(),
		UpdatedAt:   p.UpdatedAt.UTC(),
	}
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

// Resolve returns the base directory for the given project owned by the user.
func (s *ProjectStore) Resolve(ctx context.Context, projectID, userID string) (string, error) {
	p, err := s.q.GetProject(ctx, sqlc.GetProjectParams{ID: projectID, UserID: userID})
	if err != nil {
		return "", err
	}
	return p.BaseDir, nil
}

// Ensure returns the default project ID for the agent+user pair, creating the
// project (and its workspace) when none exists yet.
func (s *ProjectStore) Ensure(ctx context.Context, agentID, userID string) (string, error) {
	projects, err := s.q.ListProjects(ctx, sqlc.ListProjectsParams{AgentID: agentID, UserID: userID})
	if err != nil {
		return "", err
	}
	if len(projects) > 0 {
		return projects[0].ID, nil
	}
	agentName := agentID
	if ag, err := s.store.GetAgent(ctx, agentID); err == nil && ag.Name != "" {
		agentName = ag.Name
	}
	// Resolve the process-global home once so workspace setup and the project path
	// cannot observe different generations of the mutable test seam.
	if s.homes == nil {
		return "", ErrWorkspaceSetup
	}
	view, err := s.homes.WorkspaceView(ctx, home.WorkspaceRequest{UserID: userID, AgentID: agentID})
	if err != nil {
		return "", err
	}
	// Restore the user's assets subtree from the shared asset authority in the
	// background, so a cold pod fills its empty assets tree without blocking
	// project setup. No-op when no asset store is configured or there is no shared
	// authority (single-node, where the local tree is already the authority).
	if s.assets != nil {
		assets := s.assets
		go func() {
			if err := assets.HydrateUser(context.Background(), filepath.Join(view.DataRoot, "assets")); err != nil {
				slog.Warn("hydrate user assets failed", "home", view.PrincipalRoot, "error", err)
			}
		}()
	}
	// The default project's working tree is the agent's private area under the
	// user home (a project is owned by the agent, #442).
	baseDir := view.AgentRoot
	p, err := s.q.CreateProject(ctx, sqlc.CreateProjectParams{
		ID:      uuid.Must(uuid.NewV7()).String(),
		AgentID: agentID,
		UserID:  userID,
		Name:    agentName,
		BaseDir: baseDir,
	})
	if err != nil {
		if existing, err2 := s.q.ListProjects(ctx, sqlc.ListProjectsParams{AgentID: agentID, UserID: userID}); err2 == nil && len(existing) > 0 {
			return existing[0].ID, nil
		}
		return "", err
	}
	return p.ID, nil
}
