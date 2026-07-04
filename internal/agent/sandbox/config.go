// Package sandbox resolves an agent's sandbox config into a live session and
// provides that session's agent-facing tool projections (bash, read, write, edit).
package sandbox

import (
	"context"

	"github.com/CherryHQ/stella/internal/config"
	oauth "github.com/CherryHQ/stella/internal/credentials/oauth"
	"github.com/CherryHQ/stella/internal/vault"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

// VaultEnvLoader is the vault surface an agent session needs: binding-filtered
// env for session start and the declarable exec-time secret flow. Implemented by *vault.Service.
type VaultEnvLoader interface {
	// LoadEnvForAgentProject returns the binding-filtered env for a session
	// (projectID may be empty for agent-only sessions).
	LoadEnvForAgentProject(ctx context.Context, userID string, agentID string, projectID string) (map[string]string, error)
	ListDeclarableForAgentProject(ctx context.Context, userID string, agentID string, projectID string) ([]vault.DeclarableSecret, error)
	ResolveDeclarableEnv(ctx context.Context, userID string, agentID string, projectID string, names []string) (map[string]string, []string, error)
	RecordExecSecretUse(ctx context.Context, userID string, agentID string, sessionID string, name string, command string) error
}

// TokenEnsurer creates short-lived scoped API tokens for sandbox sessions.
type TokenEnsurer interface {
	CreateScopedToken(ctx context.Context, userID, agentID, sessionID, projectID string) (string, error)
}

// Config is passed to sandbox operations.
// It is constructed from the runner config in the parent agent package.
type Config struct {
	SandboxConfig    config.SandboxConfig
	SandboxBackendFn func(ctx context.Context) string
	Paths            Paths
	UserID           string
	GroupID          string // non-empty for group sessions; vault/token use group principal
	AgentID          string
	SessionID        string
	ProjectID        string
	SessionEnvSpecs  []pkgplugins.SessionEnvSpec
	VaultEnvLoader   VaultEnvLoader
	TokenEnsurer     TokenEnsurer
	TokenManager     *oauth.TokenManager
}
