package plugins

import (
	"context"
	"database/sql"

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
