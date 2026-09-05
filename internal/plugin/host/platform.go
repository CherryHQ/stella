package host

import (
	"context"
	"log/slog"

	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

func (h *Host) platform(pluginID string) pkgplugins.Platform {
	return pluginPlatform{host: h, pluginID: pluginID, granted: h.grantedCapabilities(pluginID)}
}

// grantedCapabilities returns the set of Platform capabilities declared by the
// plugin's registered metadata. A plugin with no metadata (or none declared)
// gets an empty set, so every gated accessor fails closed.
func (h *Host) grantedCapabilities(pluginID string) map[pkgplugins.Capability]struct{} {
	h.mu.RLock()
	defer h.mu.RUnlock()
	info, ok := h.metadataRegs[pluginID]
	if !ok || len(info.RequiredCapabilities) == 0 {
		return nil
	}
	set := make(map[pkgplugins.Capability]struct{}, len(info.RequiredCapabilities))
	for _, c := range info.RequiredCapabilities {
		set[c] = struct{}{}
	}
	return set
}

type pluginPlatform struct {
	host     *Host
	pluginID string
	granted  map[pkgplugins.Capability]struct{}
}

func (p pluginPlatform) has(c pkgplugins.Capability) bool {
	_, ok := p.granted[c]
	return ok
}

func (p pluginPlatform) Logger() *slog.Logger {
	if !p.has(pkgplugins.CapabilityLogger) {
		return nil
	}
	return p.host.Logger(p.pluginID)
}

func (p pluginPlatform) ConfigStore() pkgplugins.ConfigStore {
	if !p.has(pkgplugins.CapabilityConfigStore) {
		return nil
	}
	return scopedConfigStore{service: p.host.config, pluginID: p.pluginID}
}

func (p pluginPlatform) StateStore() pkgplugins.StateStore {
	if !p.has(pkgplugins.CapabilityStateStore) {
		return nil
	}
	return scopedStateStore{store: p.host.stateStore, pluginID: p.pluginID}
}

func (p pluginPlatform) Notifier() pkgplugins.Notifier {
	if !p.has(pkgplugins.CapabilityNotifier) {
		return nil
	}
	return p.host.Notifications()
}

func (p pluginPlatform) Auth() pkgplugins.Auth {
	if !p.has(pkgplugins.CapabilityAuth) {
		return nil
	}
	return p.host.Auth()
}

func (p pluginPlatform) RuntimeLookup() pkgplugins.RuntimeLookup {
	if !p.has(pkgplugins.CapabilityRuntimeLookup) {
		return nil
	}
	return p.host.Runtime()
}

func (p pluginPlatform) ChannelPlatform() pkgplugins.ChannelPlatform {
	if !p.has(pkgplugins.CapabilityChannelPlatform) {
		return nil
	}
	return p.host.ChannelRuntime()
}

func (p pluginPlatform) AccountEnrollment() pkgchannel.AccountEnroller {
	if !p.has(pkgplugins.CapabilityAccountEnrollment) {
		return nil
	}
	p.host.mu.RLock()
	defer p.host.mu.RUnlock()
	namespaces := channelEnrollmentNamespacesLocked(p.host.channelRegs, p.pluginID)
	if len(namespaces) != 1 || p.host.enrollment == nil {
		return nil
	}
	return scopedAccountEnroller{namespace: namespaces[0], backend: p.host.enrollment}
}

type scopedAccountEnroller struct {
	namespace string
	backend   AccountEnrollmentBackend
}

func (e scopedAccountEnroller) EnrollAccount(ctx context.Context, req pkgchannel.EnrollmentRequest) error {
	return e.backend.EnrollAccount(ctx, e.namespace, req)
}

type scopedConfigStore struct {
	service  ConfigBackend
	pluginID string
}

func (s scopedConfigStore) Get(ctx context.Context) (pkgplugins.PluginState, error) {
	return s.service.Get(ctx, s.pluginID)
}

func (s scopedConfigStore) Set(ctx context.Context, config map[string]any) error {
	return s.service.Set(ctx, s.pluginID, config)
}

type scopedStateStore struct {
	store    StateStoreBackend
	pluginID string
}

// NewScopedStateStore wraps a StateStoreBackend as a pkgplugins.StateStore
// whose calls are namespaced to the given pluginID. Used by gateway wiring
// for built-in subsystems (e.g. reflect) that need a state store but don't
// run inside the plugin runtime.
func NewScopedStateStore(store StateStoreBackend, pluginID string) pkgplugins.StateStore {
	return scopedStateStore{store: store, pluginID: pluginID}
}

func (s scopedStateStore) Get(ctx context.Context, scope pkgplugins.StateScope, key string) (map[string]any, bool, error) {
	if s.store == nil {
		return nil, false, nil
	}
	return s.store.Get(ctx, s.pluginID, scope, key)
}

func (s scopedStateStore) Set(ctx context.Context, scope pkgplugins.StateScope, key string, value map[string]any) error {
	if s.store == nil {
		return nil
	}
	return s.store.Set(ctx, s.pluginID, scope, key, value)
}

func (s scopedStateStore) Delete(ctx context.Context, scope pkgplugins.StateScope, key string) error {
	if s.store == nil {
		return nil
	}
	return s.store.Delete(ctx, s.pluginID, scope, key)
}
