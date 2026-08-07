package discord

import (
	"reflect"
	"testing"

	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

func TestDecodeConfig(t *testing.T) {
	cfg, err := DecodeConfig(map[string]any{
		"token":            "dc-token",
		"guild_id":         "123",
		"channel_id":       "456",
		"mention_only":     false,
		"respond_channels": "789, 1011",
		"enable_notify":    true,
	})
	if err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}
	if cfg.Token != "dc-token" || cfg.GuildID != "123" || cfg.ChannelID != "456" {
		t.Fatalf("decoded config = %#v", cfg)
	}
	if cfg.MentionOnly {
		t.Fatalf("explicit mention_only:false should decode false, got %#v", cfg.MentionOnly)
	}
	if cfg.RespondChannels != "789, 1011" || !cfg.EnableNotify {
		t.Fatalf("decoded config = %#v", cfg)
	}
}

// TestDecodeConfigMentionOnlyDefault asserts the safe default: when the key is
// absent, mention-only is on so a fresh config never floods a server.
func TestDecodeConfigMentionOnlyDefault(t *testing.T) {
	cfg, err := DecodeConfig(map[string]any{"token": "dc-token"})
	if err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}
	if !cfg.MentionOnly {
		t.Fatalf("mention_only default = %v, want true", cfg.MentionOnly)
	}
}

func TestRedactConfig(t *testing.T) {
	got := RedactConfig(map[string]any{
		"token":      "secret",
		"channel_id": "456",
	})
	if got["token"] != "***" {
		t.Fatalf("redacted token = %#v, want %q", got["token"], "***")
	}
	if got["channel_id"] != "456" {
		t.Fatalf("channel_id = %#v, want %q", got["channel_id"], "456")
	}
}

func TestValidateConfig(t *testing.T) {
	if msg := validateConfig(mustDecode(t, map[string]any{"token": "x"})); msg != "" {
		t.Fatalf("valid config reported error: %q", msg)
	}
	if msg := validateConfig(mustDecode(t, map[string]any{})); msg == "" {
		t.Fatal("missing token should report an error")
	}
}

func TestSplitChannelList(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"   ", nil},
		{"123", []string{"123"}},
		{" 123 , 456 ,, 789 ", []string{"123", "456", "789"}},
	}
	for _, tc := range cases {
		if got := splitChannelList(tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("splitChannelList(%q) = %#v, want %#v", tc.in, got, tc.want)
		}
	}
}

func mustDecode(t *testing.T, raw map[string]any) pkgchannel.DiscordConfig {
	t.Helper()
	cfg, err := DecodeConfig(raw)
	if err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}
	return cfg
}
