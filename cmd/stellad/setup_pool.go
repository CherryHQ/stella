package main

import (
	"context"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/pluginhost"
	coreagent "github.com/CherryHQ/stella/pkg/agent"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

// lazyServiceManager resolves the underlying ServiceManager at call time.
// Used when ServiceManager is not yet set at struct-initialization time.
type lazyServiceManager struct {
	get func() agent.ServiceManager
}

func (l *lazyServiceManager) GetService(agentID string) *agent.Service {
	sm := l.get()
	if sm == nil {
		return nil
	}
	return sm.GetService(agentID)
}

func (l *lazyServiceManager) Default() *agent.Service {
	sm := l.get()
	if sm == nil {
		return nil
	}
	return sm.Default()
}

func buildToolLifecycle(phost *pluginhost.Host) *coreagent.ToolLifecycle {
	return &coreagent.ToolLifecycle{
		BeforeCall: func(ctx context.Context, call coreagent.ToolCallContext) (coreagent.ToolCallMutation, error) {
			result, err := phost.BeforeToolCall(ctx, pkgplugins.BeforeToolCallContext{
				SessionID:  call.SessionID,
				Channel:    call.Channel,
				UserID:     call.UserID,
				AgentID:    call.AgentID,
				ToolName:   call.ToolName,
				ToolCallID: call.ToolCallID,
				Arguments:  call.Arguments,
			})
			if err != nil {
				return coreagent.ToolCallMutation{}, err
			}
			return coreagent.ToolCallMutation{
				Arguments:    result.Arguments,
				Block:        result.Block,
				BlockMessage: result.BlockMessage,
			}, nil
		},
		AfterCall: func(ctx context.Context, result coreagent.ToolResultContext) (coreagent.ToolResultMutation, error) {
			mutation, err := phost.AfterToolResult(ctx, pkgplugins.AfterToolResultContext{
				SessionID:  result.SessionID,
				Channel:    result.Channel,
				UserID:     result.UserID,
				AgentID:    result.AgentID,
				ToolName:   result.ToolName,
				ToolCallID: result.ToolCallID,
				Arguments:  result.Arguments,
				Result:     result.Result,
				IsError:    result.IsError,
				Duration:   result.Duration,
			})
			if err != nil {
				return coreagent.ToolResultMutation{}, err
			}
			return coreagent.ToolResultMutation{
				Result:  mutation.Result,
				IsError: mutation.IsError,
			}, nil
		},
	}
}

func buildProjectEnsurer(db *pgxpool.Pool, store config.Store) agent.ProjectEnsurerFunc {
	return func(ctx context.Context, agentID, userID string) (string, error) {
		q := sqlc.New(db)
		projects, err := q.ListProjects(ctx, sqlc.ListProjectsParams{AgentID: agentID, UserID: userID})
		if err != nil {
			return "", err
		}
		if len(projects) > 0 {
			return projects[0].ID, nil
		}
		agentName := agentID
		if ag, err := store.GetAgent(ctx, agentID); err == nil && ag.Name != "" {
			agentName = ag.Name
		}
		userHome, err := agent.SetupUserWorkspace(config.StellaHome(), userID, agentID)
		if err != nil {
			return "", err
		}
		// Restore the user's assets subtree from the blob mirror in the background,
		// so a cold pod fills its empty assets tree without blocking project setup.
		go func() {
			if err := agent.HydrateUserAssets(context.Background(), config.StellaHome(), userHome); err != nil {
				slog.Warn("hydrate user assets failed", "home", userHome, "error", err)
			}
		}()
		// The default project's working tree is the agent's private area under the
		// user home (a project is owned by the agent, #442).
		baseDir := agent.UserAgentDir(config.StellaHome(), userID, agentID)
		p, err := q.CreateProject(ctx, sqlc.CreateProjectParams{
			ID:      uuid.Must(uuid.NewV7()).String(),
			AgentID: agentID,
			UserID:  userID,
			Name:    agentName,
			BaseDir: baseDir,
		})
		if err != nil {
			if existing, err2 := q.ListProjects(ctx, sqlc.ListProjectsParams{AgentID: agentID, UserID: userID}); err2 == nil && len(existing) > 0 {
				return existing[0].ID, nil
			}
			return "", err
		}
		return p.ID, nil
	}
}
