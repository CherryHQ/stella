package sandbox

import (
	"context"

	"github.com/CherryHQ/stella/internal/config"
	oauth "github.com/CherryHQ/stella/internal/credentials/oauth"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

// VaultEnvLoader loads decrypted vault entries for a user as a name→value map.
type VaultEnvLoader interface {
	LoadEnv(ctx context.Context, userID string) (map[string]string, error)
}

type scopedVaultEnvLoader interface {
	LoadEnvForAgent(ctx context.Context, userID string, agentID string) (map[string]string, error)
}

type projectVaultEnvLoader interface {
	LoadEnvForAgentProject(ctx context.Context, userID string, agentID string, projectID string) (map[string]string, error)
}

type fullVaultEnvLoader interface {
	LoadFullEnvForAgent(ctx context.Context, userID string, agentID string) (map[string]string, error)
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
