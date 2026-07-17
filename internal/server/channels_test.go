package server

import (
	"testing"

	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/controlplane"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

// TestBuildPublicChannelViewsExcludesWebhook pins the user-facing channel list
// to linkable chat platforms: webhooks are inbound triggers with no identity
// to link, and the public list keys by type so duplicates would collide.
func TestBuildPublicChannelViewsExcludesWebhook(t *testing.T) {
	channels := []controlplane.PublicChannel{
		{ID: "tg", Type: pkgchannel.PlatformTelegram, Enabled: true, AgentID: "a1"},
		{ID: "wh1", Type: pkgchannel.PlatformWebhook, Enabled: true, AgentID: "a1"},
		{ID: "wh2", Type: pkgchannel.PlatformWebhook, Enabled: true, AgentID: "a1"},
	}
	enabledTypes := map[string]bool{
		pkgchannel.PlatformTelegram: true,
		pkgchannel.PlatformWebhook:  true,
	}
	agentNames := map[string]string{"a1": "Agent One"}

	views := buildPublicChannelViews(channels, enabledTypes, agentNames)

	if len(views) != 1 {
		t.Fatalf("views = %d, want 1 (webhooks excluded); got %+v", len(views), views)
	}
	if views[0].Type != pkgchannel.PlatformTelegram {
		t.Fatalf("remaining view type = %q, want telegram", views[0].Type)
	}
}

// TestChannelFromWriteRequestEnabledDefaults pins the Enabled default matrix:
// PUT-created webhooks go live like POST-created ones (no runtime to configure),
// bot channels stay disabled until configured, and an existing row's Enabled is
// preserved when the request omits the field.
func TestChannelFromWriteRequestEnabledDefaults(t *testing.T) {
	s := &Server{}
	webhook := pkgchannel.PlatformWebhook
	telegram := pkgchannel.PlatformTelegram
	enabledFalse := false

	cases := []struct {
		name        string
		req         channelWriteRequest
		existing    config.Channel
		hasExisting bool
		want        bool
	}{
		{
			name: "put-create webhook defaults enabled",
			req:  channelWriteRequest{ID: "wh1", Type: &webhook},
			want: true,
		},
		{
			name: "put-create webhook honors explicit disabled",
			req:  channelWriteRequest{ID: "wh1", Type: &webhook, Enabled: &enabledFalse},
			want: false,
		},
		{
			name: "put-create bot channel defaults disabled",
			req:  channelWriteRequest{ID: "tg", Type: &telegram},
			want: false,
		},
		{
			name:        "update webhook preserves existing disabled state",
			req:         channelWriteRequest{ID: "wh1", Type: &webhook},
			existing:    config.Channel{ID: "wh1", Type: webhook, Enabled: false},
			hasExisting: true,
			want:        false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ch := s.channelFromWriteRequest(nil, tc.req, tc.existing, tc.hasExisting)
			if ch.Enabled != tc.want {
				t.Fatalf("Enabled = %v, want %v", ch.Enabled, tc.want)
			}
		})
	}
}
