package channel

import (
	"context"
	"testing"

	"github.com/vaayne/anna/internal/config"
	pkgchannel "github.com/vaayne/anna/pkg/channel"
)

type mockPluginStore struct {
	config.Store
	plugins map[string]config.Plugin
}

func (m *mockPluginStore) GetPlugin(_ context.Context, id string) (config.Plugin, error) {
	p, ok := m.plugins[id]
	if !ok {
		return config.Plugin{}, context.DeadlineExceeded
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

	cfg := LoadConfig[pkgchannel.TelegramConfig](store, "telegram")
	if cfg == nil {
		t.Fatal("expected non-nil config")
	} else {
		if cfg.Token != "abc123" {
			t.Errorf("Token = %q, want abc123", cfg.Token)
		}
		if !cfg.EnableNotify {
			t.Error("expected EnableNotify = true")
		}
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

	cfg := LoadConfig[pkgchannel.TelegramConfig](store, "telegram")
	if cfg != nil {
		t.Error("expected nil for disabled plugin")
	}
}

func TestLoadConfigMissing(t *testing.T) {
	store := newMockStore()
	cfg := LoadConfig[pkgchannel.TelegramConfig](store, "telegram")
	if cfg != nil {
		t.Error("expected nil for missing plugin")
	}
}
