package host

import (
	"context"
	"fmt"

	"github.com/CherryHQ/stella/internal/platform/config"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

// GuestPolicyResolver interprets persisted configuration through its owning plugin.
func (h *Host) GuestPolicyResolver(channelType, rawConfig string) (pkgchannel.GuestConfig, error) {
	h.mu.RLock()
	decoder := h.channelRegs[channelType].GuestPolicy
	h.mu.RUnlock()
	if decoder == nil {
		return pkgchannel.GuestConfig{}, fmt.Errorf("channel %q does not support guest sessions", channelType)
	}
	return decoder(rawConfig)
}

func channelEnrollmentNamespacesLocked(regs map[string]pkgplugins.ChannelSpec, pluginID string) []string {
	namespaces := make([]string, 0, 1)
	for _, reg := range regs {
		if reg.PluginID == pluginID && reg.AccountEnrollment && reg.Name != "" {
			namespaces = append(namespaces, reg.Name)
		}
	}
	return namespaces
}

func (h *Host) channelEnrollmentNamespace(pluginID string) (string, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	namespaces := channelEnrollmentNamespacesLocked(h.channelRegs, pluginID)
	if len(namespaces) != 1 {
		return "", false
	}
	return namespaces[0], true
}

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
