package sandbox

import (
	"context"

	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/config"
	oauth "github.com/CherryHQ/stella/internal/credentials/oauth"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

// VaultEnvLoader loads decrypted vault entries for a user as a name→value map.
type VaultEnvLoader interface {
	LoadEnv(ctx context.Context, userID int64) (map[string]string, error)
}

// PathConfig contains the host paths passed to sandbox operations.
type PathConfig struct {
	StellaHome  string
	AgentRoot   string
	UserRoot    string
	ProjectRoot string
}

// Config is passed to sandbox operations.
// It is constructed from GoRunnerConfig in the parent agent package.
type Config struct {
	SandboxConfig    config.SandboxConfig
	SandboxBackendFn func(ctx context.Context) string
	Paths            PathConfig
	UserID           int64
	SessionEnvSpecs  []pkgplugins.SessionEnvSpec
	VaultEnvLoader   VaultEnvLoader
	TokenService     *auth.TokenService
	TokenManager     *oauth.TokenManager
}
