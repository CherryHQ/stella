package plugins

import (
	"context"
	"time"
)

// PluginState is the canonical plugin-level desired state.
type PluginState struct {
	ID      string         `json:"id"`
	Enabled bool           `json:"enabled"`
	Config  map[string]any `json:"config,omitempty"`
}

// Clone returns a shallow copy with an independent config map.
func (s PluginState) Clone() PluginState {
	s.Config = cloneMap(s.Config)
	return s
}

// ConfigStore exposes plugin-owned config persistence through a narrow interface.
type ConfigStore interface {
	Get(ctx context.Context) (PluginState, error)
	Set(ctx context.Context, config map[string]any) error
}

// RuntimeLookup resolves running runtime handles by plugin and runtime capability ID.
type RuntimeLookup interface {
	Get(pluginID string, runtimeName string) (RuntimeHandle, bool)
	Lookup(pluginID string, runtimeName string) (RuntimeHandle, bool)
}

// RuntimeHandle exposes status access to a running runtime.
type RuntimeHandle interface {
	Snapshot(ctx context.Context) (RuntimeStatus, error)
	Status(ctx context.Context) (RuntimeStatus, error)
}

// Runtime is implemented by plugin-owned long-lived runtime services.
type Runtime interface {
	Apply(ctx context.Context, desired PluginState) error
	Start(ctx context.Context, desired PluginState) error
	Reconcile(ctx context.Context, desired PluginState) error
	Stop(ctx context.Context) error
	Snapshot(ctx context.Context) (RuntimeStatus, error)
	Status(ctx context.Context) (RuntimeStatus, error)
}

// RuntimeState is the shared high-level runtime state used by host orchestration.
type RuntimeState string

const (
	RuntimeStateUnknown RuntimeState = "unknown"
	RuntimeStateStopped RuntimeState = "stopped"
	RuntimeStateRunning RuntimeState = "running"
	RuntimeStateError   RuntimeState = "error"
)

// RuntimeStatus is the minimal shared runtime status envelope.
type RuntimeStatus struct {
	State     RuntimeState   `json:"state"`
	Message   string         `json:"message,omitempty"`
	UpdatedAt time.Time      `json:"updated_at,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

// Clone returns a shallow copy with independent metadata.
func (s RuntimeStatus) Clone() RuntimeStatus {
	s.Metadata = cloneMap(s.Metadata)
	return s
}

// PromptToolInfo is a structured tool inventory item contributed to prompt building.
type PromptToolInfo struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// Clone returns a shallow copy with independent metadata.
func (i PromptToolInfo) Clone() PromptToolInfo {
	i.Metadata = cloneMap(i.Metadata)
	return i
}

// SystemPromptSection is a structured prompt contribution from a plugin.
type SystemPromptSection struct {
	Title   string `json:"title"`
	Content string `json:"content"`
}

// BeforeRunResult is the mutable per-run output from lifecycle plugins.
type BeforeRunResult struct {
	SystemPrompt string `json:"system_prompt,omitempty"`
}

// BeforeToolCallResult is the mutable pre-execution output from tool lifecycle plugins.
type BeforeToolCallResult struct {
	Arguments    map[string]any `json:"arguments,omitempty"`
	Block        bool           `json:"block,omitempty"`
	BlockMessage string         `json:"block_message,omitempty"`
}

// AfterToolResult is the mutable post-execution output from tool lifecycle plugins.
type AfterToolResult struct {
	Result  *string `json:"result,omitempty"`
	IsError *bool   `json:"is_error,omitempty"`
}

func cloneMap(src map[string]any) map[string]any {
	if len(src) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(src))
	for k, v := range src {
		out[k] = cloneValue(v)
	}
	return out
}

func cloneValue(v any) any {
	switch vv := v.(type) {
	case map[string]any:
		return cloneMap(vv)
	case []any:
		out := make([]any, len(vv))
		for i := range vv {
			out[i] = cloneValue(vv[i])
		}
		return out
	default:
		return v
	}
}
