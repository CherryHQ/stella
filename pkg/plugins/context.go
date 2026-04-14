package plugins

import (
	"context"
	"database/sql"
	"time"

	"github.com/vaayne/anna/pkg/ai"
	"github.com/vaayne/anna/pkg/channel"
)

// ToolContext is the narrow build context for tool capabilities.
type ToolContext struct {
	Platform    Platform
	State       PluginState
	WorkDir     string
	UserDataDir string
	AnnaHome    string
	HomeDir     string
	Workspace   string
	ToolsBinDir string
	Runtime     ToolRuntime
}

// ProviderContext is the narrow build context for provider capabilities.
type ProviderContext struct {
	Platform Platform
	State    PluginState
}

// HookContext is the narrow build context for hook capabilities.
type HookContext struct {
	Platform    Platform
	State       PluginState
	ToolsBinDir string
}

// ChannelContext is the narrow build context for channel capabilities.
type ChannelContext struct {
	Platform Platform
	State    PluginState
	Handler  channel.Handler
}

// MemoryContext is the narrow build context for memory capabilities.
// DB is a construction-time exception for memory providers, not a general
// plugin service surface.
type MemoryContext struct {
	Platform     Platform
	State        PluginState
	DB           *sql.DB
	AnnaHome     string
	SummarizerFn func(context.Context, string) (string, error)
}

// RuntimeContext is the narrow construction context for runtime capabilities.
type RuntimeContext struct {
	Platform Platform
	State    PluginState
}

// AdminContext is the narrow build context for plugin admin/status behavior.
type AdminContext struct {
	Platform Platform
	State    PluginState
}

// PromptInventoryContext is the narrow build context for prompt inventory contributions.
type PromptInventoryContext struct {
	Platform Platform
	State    PluginState
}

// SystemPromptContext is the shared build context for prompt contributions.
type SystemPromptContext struct {
	Platform    Platform
	State       PluginState
	AnnaHome    string
	HomeDir     string
	Workspace   string
	Cwd         string
	UserID      int64
	AgentID     string
	UserDataDir string
}

// BeforeRunContext is the narrow per-run lifecycle context exposed to plugins.
type BeforeRunContext struct {
	Platform     Platform
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

// BeforeToolCallContext is the narrow per-tool-call lifecycle context exposed to plugins.
type BeforeToolCallContext struct {
	Platform   Platform
	State      PluginState
	SessionID  string
	Channel    string
	UserID     int64
	AgentID    string
	ToolName   string
	ToolCallID string
	Arguments  map[string]any
}

// AfterToolResultContext is the narrow post-tool lifecycle context exposed to plugins.
type AfterToolResultContext struct {
	Platform   Platform
	State      PluginState
	SessionID  string
	Channel    string
	UserID     int64
	AgentID    string
	ToolName   string
	ToolCallID string
	Arguments  map[string]any
	Result     string
	IsError    bool
	Duration   time.Duration
}
