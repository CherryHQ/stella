package plugin

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"

	"github.com/CherryHQ/stella/internal/platform/config"
)

var (
	// ErrUnknownNativeID means the caller did not present an ID registered by
	// trusted Go code. User-authored names never become native capabilities.
	ErrUnknownNativeID = errors.New("plugin: unknown native id")
	// ErrNativePolicyUnavailable means the policy was not composed with every
	// backing store it needs. Admission must fail closed in this case.
	ErrNativePolicyUnavailable = errors.New("plugin: native policy unavailable")
	ErrNativeAgentDenyExists   = errors.New("plugin: native Agent deny exists")
	ErrNativeAgentNotFound     = errors.New("plugin: native Agent not found")
)

// NativeRegistry is the static registry consulted on every admission. It also
// supplies the release default used only when no global row exists yet.
type NativeRegistry interface {
	NativeDefaultEnabled(string) (defaultEnabled bool, registered bool)
	NativeIDs() []string
}

// NativeRegistryMap is the immutable composition-time registry used by the
// daemon. Keeping this tiny adapter here prevents runtime callers from
// treating persisted plugin definitions as native identities.
type NativeRegistryMap map[string]bool

func (m NativeRegistryMap) NativeDefaultEnabled(id string) (bool, bool) {
	value, ok := m[id]
	return value, ok
}

func (m NativeRegistryMap) NativeIDs() []string {
	ids := make([]string, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// NativeStore is the complete persistence port for native admission and its
// admin controls. Keeping it as one required interface prevents composing a
// policy from readers and writers backed by different stores.
type NativeStore interface {
	GetPlugin(ctx context.Context, id string) (config.Plugin, error)
	SetNativePluginEnabled(ctx context.Context, nativeID string, enabled bool) error
	GetNativeAdmission(ctx context.Context, nativeID, agentID string) (enabled, present, denied bool, err error)
	IsNativeAgentDenied(ctx context.Context, nativeID, agentID string) (bool, error)
	SetNativeAgentDeny(ctx context.Context, nativeID, agentID string) error
	DeleteNativeAgentDeny(ctx context.Context, nativeID, agentID string) error
	ListNativeAgentDenials(ctx context.Context, nativeID string) ([]NativeAgentDeny, error)
}

type NativeAgentDeny struct {
	NativeID string
	AgentID  string
}

// NativePolicy evaluates a Go-registered native ID against the deployment
// switch and the system-agent deny relation. It intentionally does not depend
// on plugin Definition, Config, or Snapshot resolution.
type NativePolicy struct {
	store    NativeStore
	registry NativeRegistry
	fence    MutationFence
}

func NewNativePolicy(store NativeStore, registry NativeRegistry) *NativePolicy {
	if store == nil || registry == nil {
		panic("plugin: native policy requires native store and registry")
	}
	return &NativePolicy{store: store, registry: registry}
}

func (p *NativePolicy) SetMutationFence(fence MutationFence) {
	if p != nil {
		p.fence = fence
	}
}

func (p *NativePolicy) mutate(ctx context.Context, fn func() error) error {
	if p == nil || p.fence == nil {
		return ErrNativePolicyUnavailable
	}
	return p.fence(ctx, fn)
}

// Allows computes a fresh decision. A storage error is returned with false, so
// callers cannot accidentally continue on stale state. The registry is
// checked every time because this is the admission security boundary.
func (p *NativePolicy) Allows(ctx context.Context, nativeID, agentID string) (bool, error) {
	if p == nil || p.store == nil || p.registry == nil || nativeID == "" || agentID == "" {
		return false, ErrNativePolicyUnavailable
	}
	defaultEnabled, registered := p.registry.NativeDefaultEnabled(nativeID)
	if !registered {
		return false, fmt.Errorf("%w: %q", ErrUnknownNativeID, nativeID)
	}
	globalEnabled, present, denied, err := p.store.GetNativeAdmission(ctx, nativeID, agentID)
	if err != nil {
		return false, fmt.Errorf("read native admission %q/%q: %w", nativeID, agentID, err)
	}
	if !present {
		globalEnabled = defaultEnabled
	}
	return globalEnabled && !denied, nil
}

func (p *NativePolicy) IsRegistered(nativeID string) bool {
	if p == nil || p.registry == nil {
		return false
	}
	_, ok := p.registry.NativeDefaultEnabled(nativeID)
	return ok
}

func (p *NativePolicy) GlobalEnabled(ctx context.Context, nativeID string) (bool, error) {
	row, err := p.GlobalPlugin(ctx, nativeID)
	return row.Enabled, err
}

// GlobalPlugin returns the trusted deployment-wide native configuration. A
// missing row uses only the static enabled default; a present row's Config is
// returned unchanged so native builders never replace real global config with
// an empty placeholder.
func (p *NativePolicy) GlobalPlugin(ctx context.Context, nativeID string) (config.Plugin, error) {
	if p == nil || p.store == nil || p.registry == nil {
		return config.Plugin{}, ErrNativePolicyUnavailable
	}
	defaultEnabled, registered := p.registry.NativeDefaultEnabled(nativeID)
	if !registered {
		return config.Plugin{}, fmt.Errorf("%w: %q", ErrUnknownNativeID, nativeID)
	}
	row, err := p.store.GetPlugin(ctx, nativeID)
	if errors.Is(err, pgx.ErrNoRows) {
		return config.Plugin{ID: nativeID, Enabled: defaultEnabled}, nil
	}
	if err != nil {
		return config.Plugin{}, err
	}
	return row, nil
}

func (p *NativePolicy) NativeIDs() []string {
	if p == nil {
		return nil
	}
	return p.registry.NativeIDs()
}

func (p *NativePolicy) SetGlobalEnabled(ctx context.Context, nativeID string, enabled bool) error {
	if p == nil || p.store == nil || p.registry == nil {
		return ErrNativePolicyUnavailable
	}
	_, registered := p.registry.NativeDefaultEnabled(nativeID)
	if !registered {
		return fmt.Errorf("%w: %q", ErrUnknownNativeID, nativeID)
	}
	return p.mutate(ctx, func() error {
		return p.store.SetNativePluginEnabled(ctx, nativeID, enabled)
	})
}

func (p *NativePolicy) SetAgentDeny(ctx context.Context, nativeID, agentID string) error {
	if !p.IsRegistered(nativeID) {
		return fmt.Errorf("%w: %q", ErrUnknownNativeID, nativeID)
	}
	return p.mutate(ctx, func() error { return p.store.SetNativeAgentDeny(ctx, nativeID, agentID) })
}

func (p *NativePolicy) DeleteAgentDeny(ctx context.Context, nativeID, agentID string) error {
	if !p.IsRegistered(nativeID) {
		return fmt.Errorf("%w: %q", ErrUnknownNativeID, nativeID)
	}
	return p.mutate(ctx, func() error { return p.store.DeleteNativeAgentDeny(ctx, nativeID, agentID) })
}

func (p *NativePolicy) ListAgentDenials(ctx context.Context, nativeID string) ([]NativeAgentDeny, error) {
	if !p.IsRegistered(nativeID) {
		return nil, fmt.Errorf("%w: %q", ErrUnknownNativeID, nativeID)
	}
	return p.store.ListNativeAgentDenials(ctx, nativeID)
}

func (p *NativePolicy) AgentDenied(ctx context.Context, nativeID, agentID string) (bool, error) {
	if !p.IsRegistered(nativeID) {
		return false, fmt.Errorf("%w: %q", ErrUnknownNativeID, nativeID)
	}
	if p == nil || p.store == nil {
		return false, ErrNativePolicyUnavailable
	}
	return p.store.IsNativeAgentDenied(ctx, nativeID, agentID)
}
