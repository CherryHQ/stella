package main

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/CherryHQ/stella/internal/platform/config"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

type fakeManagedChannelRuntimeHost struct {
	metas      []pkgplugins.PluginInfo
	configured map[string]bool
	applyErrs  map[string]error
	applyCalls []string
}

func (h *fakeManagedChannelRuntimeHost) ListRegisteredPlugins() []pkgplugins.PluginInfo {
	return append([]pkgplugins.PluginInfo(nil), h.metas...)
}

func (h *fakeManagedChannelRuntimeHost) ApplyPlugin(_ context.Context, pluginID string) error {
	h.applyCalls = append(h.applyCalls, pluginID)
	if h.applyErrs == nil {
		return nil
	}
	return h.applyErrs[pluginID]
}

func (h *fakeManagedChannelRuntimeHost) ListChannels(context.Context) ([]config.Channel, error) {
	return nil, nil
}

func (h *fakeManagedChannelRuntimeHost) ApplyChannel(context.Context, config.Channel) error {
	return nil
}

func (h *fakeManagedChannelRuntimeHost) ChannelInstanceConfigured(config.Channel) bool {
	return false
}

func (h *fakeManagedChannelRuntimeHost) ChannelConfigured(_ context.Context, name string) bool {
	if h.configured == nil {
		return false
	}
	return h.configured[name]
}

func TestApplyManagedChannelPluginsContinuesAfterStartupError(t *testing.T) {
	host := &fakeManagedChannelRuntimeHost{
		metas: []pkgplugins.PluginInfo{
			{ID: "channel/telegram", Kind: "channel", Name: "telegram"},
			{ID: "channel/qq", Kind: "channel", Name: "qq"},
		},
		configured: map[string]bool{
			"telegram": true,
			"qq":       true,
		},
		applyErrs: map[string]error{
			"channel/telegram": errors.New("EOF"),
		},
	}

	summary := applyManagedChannelPlugins(context.Background(), host)

	if summary.Registered != 2 {
		t.Fatalf("Registered = %d, want 2", summary.Registered)
	}
	if summary.Configured != 2 {
		t.Fatalf("Configured = %d, want 2", summary.Configured)
	}
	if summary.Started != 1 {
		t.Fatalf("Started = %d, want 1", summary.Started)
	}

	wantCalls := []string{"channel/telegram", "channel/qq"}
	if !reflect.DeepEqual(host.applyCalls, wantCalls) {
		t.Fatalf("ApplyPlugin calls = %v, want %v", host.applyCalls, wantCalls)
	}
}

func TestApplyManagedChannelPluginsCountsOnlyConfiguredSuccessfulChannels(t *testing.T) {
	host := &fakeManagedChannelRuntimeHost{
		metas: []pkgplugins.PluginInfo{
			{ID: "channel/telegram", Kind: "channel", Name: "telegram"},
			{ID: "channel/feishu", Kind: "channel", Name: "feishu"},
		},
		configured: map[string]bool{
			"telegram": true,
			"feishu":   false,
		},
	}

	summary := applyManagedChannelPlugins(context.Background(), host)

	if summary.Registered != 2 {
		t.Fatalf("Registered = %d, want 2", summary.Registered)
	}
	if summary.Configured != 1 {
		t.Fatalf("Configured = %d, want 1", summary.Configured)
	}
	if summary.Started != 1 {
		t.Fatalf("Started = %d, want 1", summary.Started)
	}
}
