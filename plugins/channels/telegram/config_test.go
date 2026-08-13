package telegram

import (
	"testing"

	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

func TestDecodeConfig(t *testing.T) {
	cfg, err := DecodeConfig(map[string]any{
		"token":         "tg-token",
		"channel_id":    "@stella",
		"enable_notify": true,
	})
	if err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}
	if cfg.Token != "tg-token" || cfg.ChannelID != "@stella" || !cfg.EnableNotify {
		t.Fatalf("decoded config = %#v", cfg)
	}
	if !cfg.AllowDM || cfg.AllowUnlinkedDM || !cfg.RequireMention || cfg.GuestMessageLimitPerMinute != pkgchannel.DefaultGuestMessageLimitPerMinute || cfg.GuestMaxPerChannel != pkgchannel.DefaultGuestMaxPerChannel || cfg.GuestRetentionDays != pkgchannel.DefaultGuestRetentionDays {
		t.Fatalf("defaults not applied: %#v", cfg)
	}

	explicit, err := DecodeConfig(map[string]any{"token": "tg-token", "allow_dm": false, "require_mention": false})
	if err != nil {
		t.Fatalf("DecodeConfig explicit false: %v", err)
	}
	if explicit.AllowDM || explicit.RequireMention {
		t.Fatalf("explicit false values lost: %#v", explicit)
	}
}

func TestTelegramConfigSchemaGuestBounds(t *testing.T) {
	properties := configSchema()["properties"].(map[string]any)
	for _, key := range []string{"allow_group", "allow_dm", "allow_unlinked_dm", "guest_message_limit_per_minute", "guest_max_per_channel", "guest_retention_days", "require_mention"} {
		if properties[key] == nil {
			t.Fatalf("schema missing %s", key)
		}
	}
	if validateConfig(pkgchannel.TelegramConfig{Token: "token", GuestMessageLimitPerMinute: pkgchannel.MaxGuestMessageLimitPerMinute + 1, GuestMaxPerChannel: 1, GuestRetentionDays: 1}) == "" {
		t.Fatal("out-of-range guest message limit accepted")
	}
	if cfg, err := DecodeConfig(map[string]any{}); err != nil || validateConfigValues(cfg) != "" {
		t.Fatalf("empty disabled-channel config should pass persistence validation: cfg=%#v err=%v validation=%q", cfg, err, validateConfigValues(cfg))
	}
}

func TestRedactConfig(t *testing.T) {
	got := RedactConfig(map[string]any{
		"token":         "secret",
		"channel_id":    "@stella",
		"enable_notify": true,
	})
	if got["token"] != "***" {
		t.Fatalf("redacted token = %#v, want %q", got["token"], "***")
	}
	if got["channel_id"] != "@stella" {
		t.Fatalf("channel_id = %#v, want %q", got["channel_id"], "@stella")
	}
}
