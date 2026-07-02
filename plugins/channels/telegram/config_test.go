package telegram

import "testing"

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
