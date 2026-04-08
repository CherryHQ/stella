package plugins

import (
	"context"
	"database/sql"

	"github.com/vaayne/anna/pkg/ai"
	"github.com/vaayne/anna/pkg/channel"
)

// ToolContext is the narrow build context for tool capabilities.
type ToolContext struct {
	Services    ServiceHost
	State       PluginState
	WorkDir     string
	UserDataDir string
	AnnaHome    string
	Workspace   string
	ToolsBinDir string
}

// ProviderContext is the narrow build context for provider capabilities.
type ProviderContext struct {
	Services ServiceHost
	State    PluginState
}

// HookContext is the narrow build context for hook capabilities.
type HookContext struct {
	Services    ServiceHost
	State       PluginState
	ToolsBinDir string
}

// ChannelContext is the narrow build context for channel capabilities.
type ChannelContext struct {
	Services ServiceHost
	State    PluginState
	Handler  channel.Handler
}

// MemoryContext is the narrow build context for memory capabilities.
// DB is a construction-time exception for memory providers, not a general
// plugin service surface.
type MemoryContext struct {
	Services     ServiceHost
	State        PluginState
	DB           *sql.DB
	AnnaHome     string
	SummarizerFn func(context.Context, string) (string, error)
}

// RuntimeContext is the narrow construction context for runtime capabilities.
type RuntimeContext struct {
	Services ServiceHost
	State    PluginState
}

// SystemPromptContext is the shared build context for prompt contributions.
type SystemPromptContext struct {
	Services    ServiceHost
	State       PluginState
	AnnaHome    string
	Workspace   string
	Cwd         string
	UserID      int64
	AgentID     string
	UserDataDir string
}

// BeforeRunContext is the narrow per-run lifecycle context exposed to plugins.
type BeforeRunContext struct {
	Services     ServiceHost
	State        PluginState
	SessionID    string
	Channel      string
	UserID       int64
	AgentID      string
	Model        string
	MessageText  string
	SystemPrompt string
	History      []ai.Message
}
