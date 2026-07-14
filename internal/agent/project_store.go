package agent

import (
	"context"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/asset"
	"github.com/CherryHQ/stella/internal/config"
	sqlc "github.com/CherryHQ/stella/pkg/db/sqlc"
)

// ProjectStore resolves project base directories and ensures a default
// project exists for an agent+user pair. Resolve satisfies
// ProjectResolverFunc and Ensure satisfies ProjectEnsurerFunc.
type ProjectStore struct {
	q      *sqlc.Queries
	store  config.Store
	assets *asset.Store
}

// NewProjectStore builds a ProjectStore over the given pool and config store.
// assets is the authoritative asset store used to hydrate a cold pod's asset
// tree when a project is first created; it may be nil (hydration is skipped).
func NewProjectStore(db *pgxpool.Pool, store config.Store, assets *asset.Store) *ProjectStore {
	return &ProjectStore{q: sqlc.New(db), store: store, assets: assets}
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
	stellaHome := config.StellaHome()
	userHome, err := SetupUserWorkspace(stellaHome, userID, agentID)
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
			if err := assets.HydrateUser(context.Background(), UserAssetsDir(userHome)); err != nil {
				slog.Warn("hydrate user assets failed", "home", userHome, "error", err)
			}
		}()
	}
	// The default project's working tree is the agent's private area under the
	// user home (a project is owned by the agent, #442).
	baseDir := UserAgentDir(stellaHome, userID, agentID)
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
