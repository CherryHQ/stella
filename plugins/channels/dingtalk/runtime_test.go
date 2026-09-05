package dingtalk

import (
	"testing"

	"github.com/CherryHQ/stella/pkg/channel"
)

func TestDecodeAndRedactConfig(t *testing.T) {
	cfg, err := DecodeConfig(map[string]any{
		"client_id":                      "ding-app",
		"client_secret":                  "ding-secret",
		"allow_group":                    true,
		"allow_unlinked_dm":              true,
		"guest_message_limit_per_minute": 12,
		"guest_max_per_channel":          50,
		"guest_retention_days":           7,
	})
	if err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}
	if cfg.ClientID != "ding-app" || cfg.ClientSecret != "ding-secret" || !cfg.AllowGroup || !cfg.AllowDM || !cfg.AllowUnlinkedDM || !cfg.RequireMention {
		t.Fatalf("decoded config = %#v", cfg)
	}
	if cfg.GuestMessageLimitPerMinute != 12 || cfg.GuestMaxPerChannel != 50 || cfg.GuestRetentionDays != 7 {
		t.Fatalf("guest config = %#v", cfg)
	}
	redacted := RedactConfig(map[string]any{"client_id": "ding-app", "client_secret": "ding-secret"})
	if redacted["client_secret"] != "***" || redacted["client_id"] != "ding-app" {
		t.Fatalf("redacted config = %#v", redacted)
	}
}

func TestValidateConfig(t *testing.T) {
	valid := DingTalkConfig{
		ClientID: "id", ClientSecret: "secret",
		GuestMessageLimitPerMinute: channel.DefaultGuestMessageLimitPerMinute,
		GuestMaxPerChannel:         channel.DefaultGuestMaxPerChannel,
		GuestRetentionDays:         channel.DefaultGuestRetentionDays,
	}
	if got := validateConfig(valid); got != "" {
		t.Fatalf("valid config rejected: %s", got)
	}
	valid.ClientSecret = ""
	if got := validateConfig(valid); got == "" {
		t.Fatal("missing secret accepted")
	}
}
