package plugins

import (
	"context"
	"encoding/json"

	"github.com/vaayne/anna/pkg/channel"
	"github.com/vaayne/anna/pkg/hooks"
	"github.com/vaayne/anna/pkg/memory"
	"github.com/vaayne/anna/pkg/providers"
	"github.com/vaayne/anna/pkg/tools"
)

// ToolSpec declares a tool capability owned by a plugin.
type ToolSpec struct {
	PluginID    string
	Name        string
	Description string
	Required    bool
	Build       func(ctx ToolContext) (tools.Tool, error)
}

// ProviderMeta contains provider display metadata.
type ProviderMeta struct {
	Name       string
	DefaultURL string
}

// ProviderSpec declares a provider capability owned by a plugin.
type ProviderSpec struct {
	PluginID string
	Name     string
	Meta     ProviderMeta
	Build    func(ctx ProviderContext) (providers.ProviderAdapter, error)
}

// ChannelSpec declares a channel capability owned by a plugin.
type ChannelSpec struct {
	PluginID              string
	Name                  string
	SupportsNotifications bool
	Configured            func(raw map[string]any) bool
	NotificationsEnabled  func(raw map[string]any) bool
	Build                 func(ctx ChannelContext) (channel.Channel, error)
}

// HookSpec declares a hook capability owned by a plugin.
type HookSpec struct {
	PluginID string
	Name     string
	Build    func(ctx HookContext) (hooks.HookPlugin, error)
}

// MemorySpec declares a memory capability owned by a plugin.
type MemorySpec struct {
	PluginID string
	Name     string
	Build    func(ctx context.Context, build MemoryContext) (memory.Provider, error)
}

// RuntimeSpec declares a managed runtime capability owned by a plugin.
type RuntimeSpec struct {
	PluginID string
	Name     string
	Factory  func(ctx RuntimeContext) (Runtime, error)
	Build    func(ctx RuntimeContext) (Runtime, error)
}

// AdminSpec declares plugin-owned admin behavior: config defaults, schema, validation,
// redaction, and status.
type AdminSpec struct {
	PluginID      string
	DefaultConfig func() map[string]any
	Schema        map[string]any
	Validate      func(raw map[string]any) error
	Redact        func(raw map[string]any) map[string]any
	Get           func(ctx context.Context) (any, error)
	Status        func(ctx context.Context, build AdminContext) (any, error)
}

// Defaults returns a defensive copy of the registered default config.
func (r AdminSpec) Defaults() map[string]any {
	if r.DefaultConfig == nil {
		return map[string]any{}
	}
	return cloneMap(r.DefaultConfig())
}

// Redacted returns a redacted copy of raw config, or a cloned copy when no redactor is set.
func (r AdminSpec) Redacted(raw map[string]any) map[string]any {
	if r.Redact == nil {
		return cloneMap(raw)
	}
	return cloneMap(r.Redact(cloneMap(raw)))
}

// SchemaDefinition returns a defensive deep copy of the registered config schema.
func (r AdminSpec) SchemaDefinition() map[string]any {
	if len(r.Schema) == 0 {
		return map[string]any{}
	}
	b, err := json.Marshal(r.Schema)
	if err != nil {
		return cloneMap(r.Schema)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return cloneMap(r.Schema)
	}
	return out
}

// PromptInventorySpec declares structured tool inventory contribution.
type PromptInventorySpec struct {
	PluginID       string
	Name           string
	LegacyGetTools func(ctx context.Context) ([]PromptToolInfo, error)
	GetTools       func(ctx context.Context, build PromptInventoryContext) ([]PromptToolInfo, error)
}

// SystemPromptSpec declares prompt contribution owned by a plugin.
type SystemPromptSpec struct {
	PluginID string
	Name     string
	Required bool
	Build    func(ctx context.Context, build SystemPromptContext) (SystemPromptSection, error)
}

// BeforeRunSpec declares a dynamic per-run lifecycle hook owned by a plugin.
type BeforeRunSpec struct {
	PluginID string
	Name     string
	Order    int
	Required bool
	Run      func(ctx context.Context, build BeforeRunContext) (BeforeRunResult, error)
}

// BeforeToolCallSpec declares a pre-tool lifecycle hook owned by a plugin.
type BeforeToolCallSpec struct {
	PluginID string
	Name     string
	Order    int
	Required bool
	Run      func(ctx context.Context, build BeforeToolCallContext) (BeforeToolCallResult, error)
}

// AfterToolResultSpec declares a post-tool lifecycle hook owned by a plugin.
type AfterToolResultSpec struct {
	PluginID string
	Name     string
	Order    int
	Required bool
	Run      func(ctx context.Context, build AfterToolResultContext) (AfterToolResult, error)
}
