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

// TokenEnsurer ensures a per-user API token exists in the vault before sandbox
// env loading. Implementations must be idempotent.
type TokenEnsurer interface {
	EnsureAutoToken(ctx context.Context, userID string) error
}

type tokenEnvEnsurer interface {
	EnsureAutoTokenEnv(ctx context.Context, userID string) (map[string]string, error)
}

// Config is passed to sandbox operations.
// It is constructed from the runner config in the parent agent package.
type Config struct {
	SandboxConfig    config.SandboxConfig
	SandboxBackendFn func(ctx context.Context) string
	Paths            Paths
	UserID           string
	AgentID          string
	SessionID        string
	SessionEnvSpecs  []pkgplugins.SessionEnvSpec
	VaultEnvLoader   VaultEnvLoader
	TokenEnsurer     TokenEnsurer
	TokenManager     *oauth.TokenManager
}
