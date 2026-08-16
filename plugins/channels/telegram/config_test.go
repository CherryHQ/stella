package telegram

import (
	"slices"
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
	for _, key := range []string{"allow_group", "allowed_chat_ids", "allowed_topic_ids", "allow_dm", "allow_unlinked_dm", "guest_message_limit_per_minute", "guest_max_per_channel", "guest_retention_days", "require_mention"} {
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

func TestDecodeConfigNormalizesGroupAllowlists(t *testing.T) {
	cfg, err := DecodeConfig(map[string]any{
		"token":             "tg-token",
		"allowed_chat_ids":  []any{" -100 ", "-100", "-200"},
		"allowed_topic_ids": []any{"-100:42", " -100:42 ", "-200:7"},
	})
	if err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}
	if got, want := cfg.AllowedChatIDs, []string{"-100", "-200"}; !slices.Equal(got, want) {
		t.Fatalf("AllowedChatIDs = %#v, want %#v", got, want)
	}
	if got, want := cfg.AllowedTopicIDs, []string{"-100:42", "-200:7"}; !slices.Equal(got, want) {
		t.Fatalf("AllowedTopicIDs = %#v, want %#v", got, want)
	}
}

func TestDecodeConfigRejectsBlankAllowlistEntry(t *testing.T) {
	if _, err := DecodeConfig(map[string]any{"token": "tg-token", "allowed_chat_ids": []any{" "}}); err == nil {
		t.Fatal("blank chat allowlist entry accepted")
	}
	if _, err := DecodeConfig(map[string]any{"token": "tg-token", "allowed_topic_ids": []any{""}}); err == nil {
		t.Fatal("blank topic allowlist entry accepted")
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
