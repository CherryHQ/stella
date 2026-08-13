package plugins

import (
	"time"

	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/sandbox"
)

// ToolContext is the narrow build context for tool capabilities.
type ToolContext struct {
	Platform Platform
	Runtime  sandbox.Session
}

// ToolBuildContext carries the active runtime for per-session tool construction.
type ToolBuildContext struct {
	Runtime sandbox.Session
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
// It carries logical identity and immutable plugin/policy state only; filesystem
// access belongs to the active Session or to an explicitly captured snapshot.
type SystemPromptContext struct {
	Platform Platform
	State    PluginState
	UserID   string
	AgentID  string
	// RegisteredPluginIDs and EnabledPluginIDs describe plugin visibility for
	// prompt builders that need plugin-state-aware output such as skill catalogs.
	RegisteredPluginIDs []string
	EnabledPluginIDs    []string
	// DisabledSkillRefs is copied from the Agent runner snapshot. Prompt and
	// Skills-tool reads receive the same value for the life of that runner.
	DisabledSkillRefs []string
}

// SessionPluginView is the runner-facing view of enabled plugin-owned session
// setup plus the plugin visibility state prompt builders may need.
type SessionPluginView struct {
	RegisteredPluginIDs []string
	EnabledPluginIDs    []string
	SessionEnvSpecs     []SessionEnvSpec
}

// BeforeRunContext is the narrow per-run lifecycle context exposed to plugins.
type BeforeRunContext struct {
	Platform     Platform
	State        PluginState
	SessionID    string
	Channel      string
	UserID       string
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
	UserID     string
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
	UserID     string
	AgentID    string
	ToolName   string
	ToolCallID string
	Arguments  map[string]any
	Result     string
	IsError    bool
	Duration   time.Duration
}
