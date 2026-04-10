package pluginhost

import (
	"context"

	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

func (h *Host) ChannelConfigured(ctx context.Context, name string) bool {
	state, reg, ok := h.channelState(ctx, name)
	if !ok || !state.Enabled {
		return false
	}
	if reg.Configured == nil {
		return true
	}
	return reg.Configured(cloneMap(state.Config))
}

func (h *Host) ChannelNotificationsEnabled(ctx context.Context, name string) bool {
	state, reg, ok := h.channelState(ctx, name)
	if !ok || !state.Enabled {
		return false
	}
	if reg.NotificationsEnabled == nil {
		return false
	}
	return reg.NotificationsEnabled(cloneMap(state.Config))
}

func (h *Host) channelState(ctx context.Context, name string) (state pkgplugins.PluginState, reg pkgplugins.ChannelSpec, ok bool) {
	h.mu.RLock()
	reg, ok = h.channelRegs[name]
	h.mu.RUnlock()
	if !ok {
		return pkgplugins.PluginState{}, pkgplugins.ChannelSpec{}, false
	}
	desired, err := h.DesiredState(ctx, reg.PluginID)
	if err != nil {
		return pkgplugins.PluginState{}, pkgplugins.ChannelSpec{}, false
	}
	return desired.Clone(), reg, true
}
