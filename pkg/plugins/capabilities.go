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

// ToolRegistration registers a tool capability owned by a plugin.
type ToolRegistration struct {
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

// ProviderRegistration registers a provider capability owned by a plugin.
type ProviderRegistration struct {
	PluginID string
	Name     string
	Meta     ProviderMeta
	Build    func(ctx ProviderContext) (providers.ProviderAdapter, error)
}

// ChannelRegistration registers a channel capability owned by a plugin.
type ChannelRegistration struct {
	PluginID              string
	Name                  string
	SupportsNotifications bool
	Build                 func(ctx ChannelContext) (channel.Channel, error)
}

// HookRegistration registers a hook capability owned by a plugin.
type HookRegistration struct {
	PluginID string
	Name     string
	Build    func(ctx HookContext) (hooks.HookPlugin, error)
}

// MemoryRegistration registers a memory capability owned by a plugin.
type MemoryRegistration struct {
	PluginID string
	Name     string
	Build    func(ctx context.Context, build MemoryContext) (memory.Provider, error)
}

// RuntimeRegistration registers a managed runtime capability owned by a plugin.
type RuntimeRegistration struct {
	PluginID string
	Name     string
	Factory  func(ctx RuntimeContext) (ManagedRuntime, error)
}

// ConfigRegistration registers plugin-owned config defaults and validation.
type ConfigRegistration struct {
	PluginID      string
	DefaultConfig func() map[string]any
	Schema        map[string]any
	Validate      func(raw map[string]any) error
	Redact        func(raw map[string]any) map[string]any
}

// Defaults returns a defensive copy of the registered default config.
func (r ConfigRegistration) Defaults() map[string]any {
	if r.DefaultConfig == nil {
		return map[string]any{}
	}
	return cloneMap(r.DefaultConfig())
}

// Redacted returns a redacted copy of raw config, or a cloned copy when no redactor is set.
func (r ConfigRegistration) Redacted(raw map[string]any) map[string]any {
	if r.Redact == nil {
		return cloneMap(raw)
	}
	return cloneMap(r.Redact(cloneMap(raw)))
}

// SchemaDefinition returns a defensive deep copy of the registered config schema.
func (r ConfigRegistration) SchemaDefinition() map[string]any {
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

// StatusRegistration registers plugin-owned status reporting.
type StatusRegistration struct {
	PluginID string
	Get      func(ctx context.Context) (any, error)
}

// PromptInventoryRegistration registers structured tool inventory contribution.
type PromptInventoryRegistration struct {
	PluginID string
	Name     string
	GetTools func(ctx context.Context) ([]PromptToolInfo, error)
}
