package plugins

import (
	"time"

	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/sandbox"
	"github.com/CherryHQ/stella/pkg/tools"
)

// ToolContext is the narrow build context for tool capabilities.
type ToolContext struct {
	Platform Platform
	Runtime  sandbox.Session
}

// ToolBuildContext carries the active runtime for per-session tool construction.
type ToolBuildContext struct {
	Runtime sandbox.Session
	AgentID string
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

// PluginResourceIdentity identifies the selected definition/config revision
// behind a session resource. ConfigID is empty only for a resource that has no
// selected config; callers that need an installable resource must reject that
// state rather than inventing a cache identity.
type PluginResourceIdentity struct {
	PluginID string
	ConfigID string
	Scope    string
	Revision int64
}

// PluginBinarySpec is a selected plugin binary resource. It is a projection
// only: the host installer must not consume user-scoped values from this type.
type PluginBinarySpec struct {
	PluginResourceIdentity
	// PackageDigest identifies the immutable published package envelope that
	// declared this binary. It is part of selection identity, while the
	// artifact identity remains based on the effective install inputs.
	PackageDigest string
	Name          string
	Tool          string
	Version       string
	Options       map[string]any
}

// MCPDirectoryEntry is the immutable, model-facing identity of one selected
// MCP child and the catalog tools successfully projected for it. It deliberately
// carries no endpoint or credential material. The config revision and complete
// executable declarations detect a directory change without exposing secrets.
type MCPDirectoryEntry struct {
	PluginResourceIdentity
	ServerKey string
	Tools     []MCPToolDescriptor
	// Ready is explicit because an empty catalog is a successful probe, while
	// a skipped or failed server may also have zero tools.
	Ready       bool
	Status      string
	StatusError string
}

type MCPToolDescriptor struct {
	Name        string
	Description string
	InputSchema map[string]any
	Annotations map[string]any
}

// MCPToolSnapshot is the provider result consumed by runner construction. The
// directory and tools are one observation-backed projection and must not be
// read independently.
type MCPToolSnapshot struct {
	Tools               []tools.Tool
	Directory           []MCPDirectoryEntry
	SuccessfulPluginIDs []string
}

// SessionPluginView is the runner-facing view of selected plugin-owned session
// setup, resources, and plugin visibility state.
type SessionPluginView struct {
	RegisteredPluginIDs []string
	// ExposedPluginIDs identify the enabled Agent packages in this snapshot.
	ExposedPluginIDs []string
	SessionEnvSpecs  []SessionEnvSpec
	BinarySpecs      []PluginBinarySpec
	PromptSections   []SystemPromptSection
	// MCPDirectory is the exact selected MCP child/tool directory used by the
	// runner. It lives here because it is an observation-backed capability set,
	// not part of the authored plugin snapshot.
	MCPDirectory []MCPDirectoryEntry
	// SuccessfulPluginIDs records packages for which runtime resource discovery
	// succeeded. An enabled package can be absent when its remote resources are
	// unavailable, so enabled visibility alone is not a sufficient cache key.
	SuccessfulPluginIDs []string
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
