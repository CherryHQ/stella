package plugins

import (
	"context"
	"time"
)

// PluginState is the canonical plugin-level desired state.
type PluginState struct {
	ID      string         `json:"id"`
	Enabled bool           `json:"enabled"`
	Config  map[string]any `json:"config"`
}

// Clone returns a shallow copy with an independent config map.
func (s PluginState) Clone() PluginState {
	s.Config = cloneMap(s.Config)
	return s
}

// ConfigService exposes plugin-owned config persistence through a narrow interface.
type ConfigService interface {
	Get(ctx context.Context, pluginID string) (PluginState, error)
	Set(ctx context.Context, pluginID string, config map[string]any) error
}

// RuntimeLookup resolves running runtime handles by plugin and runtime capability ID.
type RuntimeLookup interface {
	Get(pluginID string, runtimeName string) (RuntimeHandle, bool)
}

// RuntimeHandle exposes snapshot access to a running runtime.
type RuntimeHandle interface {
	Snapshot(ctx context.Context) (RuntimeSnapshot, error)
}

// ManagedRuntime is implemented by plugin-owned long-lived runtime services.
type ManagedRuntime interface {
	Apply(ctx context.Context, desired PluginState) error
	Stop(ctx context.Context) error
	Snapshot(ctx context.Context) (RuntimeSnapshot, error)
}

// RuntimeState is the shared high-level runtime state used by host orchestration.
type RuntimeState string

const (
	RuntimeStateUnknown RuntimeState = "unknown"
	RuntimeStateStopped RuntimeState = "stopped"
	RuntimeStateRunning RuntimeState = "running"
	RuntimeStateError   RuntimeState = "error"
)

// RuntimeSnapshot is the minimal shared runtime snapshot envelope.
type RuntimeSnapshot struct {
	State     RuntimeState   `json:"state"`
	Message   string         `json:"message,omitempty"`
	UpdatedAt time.Time      `json:"updated_at,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

// Clone returns a shallow copy with independent metadata.
func (s RuntimeSnapshot) Clone() RuntimeSnapshot {
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

func cloneMap(src map[string]any) map[string]any {
	if len(src) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}
