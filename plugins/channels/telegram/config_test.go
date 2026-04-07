package telegram

import "testing"

func TestDecodeConfig(t *testing.T) {
	cfg, err := DecodeConfig(map[string]any{
		"token":         "tg-token",
		"channel_id":    "@anna",
		"group_mode":    "mention",
		"enable_notify": true,
	})
	if err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}
	if cfg.Token != "tg-token" || cfg.ChannelID != "@anna" || cfg.GroupMode != "mention" || !cfg.EnableNotify {
		t.Fatalf("decoded config = %#v", cfg)
	}
}

func TestRedactConfig(t *testing.T) {
	got := RedactConfig(map[string]any{
		"token":         "secret",
		"channel_id":    "@anna",
		"enable_notify": true,
	})
	if got["token"] != "***" {
		t.Fatalf("redacted token = %#v, want %q", got["token"], "***")
	}
	if got["channel_id"] != "@anna" {
		t.Fatalf("channel_id = %#v, want %q", got["channel_id"], "@anna")
	}
}
