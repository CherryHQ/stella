package plugins

import (
	"context"
	"database/sql"
	"time"

	"github.com/vaayne/anna/pkg/ai"
	"github.com/vaayne/anna/pkg/channel"
	"github.com/vaayne/anna/pkg/sandbox"
)

// ToolPaths is the tool-facing session path surface.
// UserRoot is the writable execution root and process HOME.
// ProjectRoot is the tool-facing current-project directory; relative paths in project-aware tools
// resolve against it. AnnaHome and AgentRoot are discovery roots.
type ToolPaths struct {
	UserRoot    string
	ToolsBinDir string
	AnnaHome    string
	AgentRoot   string
	ProjectRoot string // tool-facing project root for relative path resolution
}

// ToolContext is the narrow build context for tool capabilities.
type ToolContext struct {
	Platform Platform
	Paths    ToolPaths
	Runtime  sandbox.Session
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
// HomeDir is host-scoped discovery context; UserRoot is the runtime writable root.
type SystemPromptContext struct {
	Platform    Platform
	State       PluginState
	AnnaHome    string
	HomeDir     string
	AgentRoot   string
	ProjectRoot string
	UserID      int64
	AgentID     string
	UserRoot    string
	// RegisteredPluginIDs and EnabledPluginIDs describe plugin visibility for
	// prompt builders that need plugin-state-aware output such as skill catalogs.
	RegisteredPluginIDs []string
	EnabledPluginIDs    []string

	// EnabledBuiltinSkills is the per-agent allowlist of builtin (system-scope)
	// skills that should appear in the prompt catalog. The always-on "anna"
	// skill is visible to every agent regardless of this list.
	EnabledBuiltinSkills []string
}

// SessionPluginView is the runner-facing view of enabled plugin-owned session
// setup plus the plugin visibility state prompt builders may need.
type SessionPluginView struct {
	RegisteredPluginIDs []string
	EnabledPluginIDs    []string
	WrapperSpecs        []WrapperSpec
	SessionEnvSpecs     []SessionEnvSpec
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
