package server

import (
	"testing"

	"github.com/CherryHQ/stella/internal/config"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

// TestBuildPublicChannelViewsExcludesWebhook pins the user-facing channel list
// to linkable chat platforms: webhooks are inbound triggers with no identity
// to link, and the public list keys by type so duplicates would collide.
func TestBuildPublicChannelViewsExcludesWebhook(t *testing.T) {
	channels := []config.Channel{
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
