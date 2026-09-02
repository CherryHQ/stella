package host

import (
	"context"

	"github.com/CherryHQ/stella/internal/config"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

func (h *Host) ListChannels(ctx context.Context) ([]config.Channel, error) {
	return h.store.ListChannels(ctx)
}

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

func (h *Host) ChannelInstanceConfigured(channel config.Channel) bool {
	if !channel.Enabled {
		return false
	}
	channelType := channel.Type
	if channelType == "" {
		channelType = channel.ID
	}
	state := pkgplugins.PluginState{
		ID:      channel.ID,
		Enabled: channel.Enabled,
		Config:  configMapFromJSON(channel.Config),
	}
	h.mu.RLock()
	reg, ok := h.channelRegs[channelType]
	h.mu.RUnlock()
	if !ok {
		return false
	}
	if reg.Configured == nil {
		return true
	}
	return reg.Configured(cloneMap(state.Config))
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
