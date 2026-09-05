package channel

import (
	"context"
	"testing"

	"github.com/CherryHQ/stella/internal/platform/config"
	telegram "github.com/CherryHQ/stella/plugins/channels/telegram"
)

type mockPluginStore struct {
	config.Store
	channels map[string]config.Channel
}

func (m *mockPluginStore) GetChannel(_ context.Context, id string) (config.Channel, error) {
	ch, ok := m.channels[id]
	if !ok {
		return config.Channel{}, context.DeadlineExceeded
	}
	return ch, nil
}

func newMockStore(channels ...config.Channel) *mockPluginStore {
	m := &mockPluginStore{channels: make(map[string]config.Channel)}
	for _, ch := range channels {
		m.channels[ch.ID] = ch
	}
	return m
}

func TestLoadConfig(t *testing.T) {
	store := newMockStore(config.Channel{
		ID:      "telegram",
		Type:    "telegram",
		Enabled: true,
		Config:  `{"token":"abc123","enable_notify":true}`,
	})

	cfg := LoadConfig[telegram.TelegramConfig](store, "telegram")
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
	store := newMockStore(config.Channel{
		ID:      "telegram",
		Type:    "telegram",
		Enabled: false,
		Config:  `{"token":"abc123"}`,
	})

	cfg := LoadConfig[telegram.TelegramConfig](store, "telegram")
	if cfg != nil {
		t.Error("expected nil for disabled plugin")
	}
}

func TestLoadConfigMissing(t *testing.T) {
	store := newMockStore()
	cfg := LoadConfig[telegram.TelegramConfig](store, "telegram")
	if cfg != nil {
		t.Error("expected nil for missing plugin")
	}
}
