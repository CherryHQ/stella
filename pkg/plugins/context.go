package plugins

import (
	"time"

	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/sandbox"
)

// ToolPaths is the tool-facing session path surface.
// UserRoot is the shared principal home. WorkspaceRoot is the per-agent execution
// root and process HOME/cwd. ProjectRoot is the tool-facing current-project
// directory; relative paths in project-aware tools resolve against it. StellaHome
// and AgentRoot are discovery roots.
type ToolPaths struct {
	UserRoot      string
	WorkspaceRoot string
	ToolsBinDir   string
	StellaHome    string
	AgentRoot     string
	ProjectRoot   string // tool-facing project root for relative path resolution
}

// ToolContext is the narrow build context for tool capabilities.
type ToolContext struct {
	Platform Platform
	Paths    ToolPaths
	Runtime  sandbox.Session
}

// ToolBuildContext carries per-session configuration for tool construction.
// It is the runner-facing subset of ToolContext without the Platform field.
type ToolBuildContext struct {
	Paths   ToolPaths
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
// HomeDir is host-scoped discovery context; UserRoot is the shared principal home,
// and WorkspaceRoot is the per-agent runtime workspace.
type SystemPromptContext struct {
	Platform      Platform
	State         PluginState
	StellaHome    string
	HomeDir       string
	AgentRoot     string
	ProjectRoot   string
	UserID        string
	AgentID       string
	UserRoot      string
	WorkspaceRoot string
	// SkillStore is a direct shortcut for callers that have a SkillStore but no Platform.
	// BuildPromptSection uses this when Platform is nil.
	SkillStore SkillStore
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
	GroupID      string
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
