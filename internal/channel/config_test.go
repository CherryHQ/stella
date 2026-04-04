package channel

import (
	"context"
	"testing"

	"github.com/vaayne/anna/internal/config"
)

// mockPluginStore implements only the methods needed for LoadConfig/HasValidConfig/IsNotifyEnabled.
type mockPluginStore struct {
	config.Store
	plugins map[string]config.Plugin
}

func (m *mockPluginStore) GetPlugin(_ context.Context, id string) (config.Plugin, error) {
	p, ok := m.plugins[id]
	if !ok {
		return config.Plugin{}, context.DeadlineExceeded // simulate not found
	}
	return p, nil
}

func newMockStore(plugins ...config.Plugin) *mockPluginStore {
	m := &mockPluginStore{plugins: make(map[string]config.Plugin)}
	for _, p := range plugins {
		m.plugins[p.ID] = p
	}
	return m
}

func TestLoadConfig(t *testing.T) {
	store := newMockStore(config.Plugin{
		ID:      "channel/telegram",
		Kind:    config.PluginKindChannel,
		Name:    "telegram",
		Enabled: true,
		Config:  map[string]any{"token": "abc123", "enable_notify": true},
	})

	cfg := LoadConfig[TelegramConfig](store, "telegram")
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if cfg.Token != "abc123" {
		t.Errorf("Token = %q, want abc123", cfg.Token)
	}
	if !cfg.EnableNotify {
		t.Error("expected EnableNotify = true")
	}
}

func TestLoadConfigDisabled(t *testing.T) {
	store := newMockStore(config.Plugin{
		ID:      "channel/telegram",
		Kind:    config.PluginKindChannel,
		Name:    "telegram",
		Enabled: false,
		Config:  map[string]any{"token": "abc123"},
	})

	cfg := LoadConfig[TelegramConfig](store, "telegram")
	if cfg != nil {
		t.Error("expected nil for disabled plugin")
	}
}

func TestLoadConfigMissing(t *testing.T) {
	store := newMockStore()
	cfg := LoadConfig[TelegramConfig](store, "telegram")
	if cfg != nil {
		t.Error("expected nil for missing plugin")
	}
}

func TestHasValidConfig(t *testing.T) {
	tests := []struct {
		name   string
		plugin config.Plugin
		want   bool
	}{
		{
			"telegram valid",
			config.Plugin{ID: "channel/telegram", Kind: "channel", Name: "telegram", Enabled: true, Config: map[string]any{"token": "abc"}},
			true,
		},
		{
			"telegram no token",
			config.Plugin{ID: "channel/telegram", Kind: "channel", Name: "telegram", Enabled: true, Config: map[string]any{}},
			false,
		},
		{
			"qq valid",
			config.Plugin{ID: "channel/qq", Kind: "channel", Name: "qq", Enabled: true, Config: map[string]any{"app_id": "a", "app_secret": "b"}},
			true,
		},
		{
			"qq missing secret",
			config.Plugin{ID: "channel/qq", Kind: "channel", Name: "qq", Enabled: true, Config: map[string]any{"app_id": "a"}},
			false,
		},
		{
			"feishu valid",
			config.Plugin{ID: "channel/feishu", Kind: "channel", Name: "feishu", Enabled: true, Config: map[string]any{"app_id": "a", "app_secret": "b"}},
			true,
		},
		{
			"weixin valid",
			config.Plugin{ID: "channel/weixin", Kind: "channel", Name: "weixin", Enabled: true, Config: map[string]any{"bot_token": "t"}},
			true,
		},
		{
			"unknown platform",
			config.Plugin{ID: "channel/unknown", Kind: "channel", Name: "unknown", Enabled: true, Config: map[string]any{"key": "val"}},
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newMockStore(tt.plugin)
			got := HasValidConfig(store, tt.plugin.Name)
			if got != tt.want {
				t.Errorf("HasValidConfig(%q) = %v, want %v", tt.plugin.Name, got, tt.want)
			}
		})
	}
}

func TestIsNotifyEnabled(t *testing.T) {
	store := newMockStore(
		config.Plugin{ID: "channel/telegram", Kind: "channel", Name: "telegram", Enabled: true, Config: map[string]any{"token": "t", "enable_notify": true}},
		config.Plugin{ID: "channel/qq", Kind: "channel", Name: "qq", Enabled: true, Config: map[string]any{"app_id": "a", "app_secret": "s", "enable_notify": false}},
	)

	if !IsNotifyEnabled(store, "telegram") {
		t.Error("expected telegram notify enabled")
	}
	if IsNotifyEnabled(store, "qq") {
		t.Error("expected qq notify disabled")
	}
	if IsNotifyEnabled(store, "missing") {
		t.Error("expected false for missing platform")
	}
}
