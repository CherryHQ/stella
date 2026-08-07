package channel

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

type guestResolutionConfigStore struct {
	config.Store
	channel config.Channel
}

func (s *guestResolutionConfigStore) GetChannel(context.Context, string) (config.Channel, error) {
	return s.channel, nil
}

func (s *guestResolutionConfigStore) ListEnabledAgents(context.Context) ([]config.Agent, error) {
	return nil, nil
}

type guestResolutionAuthStore struct{ channelAuthStore }

func (guestResolutionAuthStore) GetChannelIdentityByPlatform(context.Context, string, string) (auth.ChannelIdentity, error) {
	return auth.ChannelIdentity{}, pgx.ErrNoRows
}

func (guestResolutionAuthStore) GetLoginIdentityByProvider(context.Context, string, string) (auth.LoginIdentity, error) {
	return auth.LoginIdentity{}, pgx.ErrNoRows
}

type guestResolutionServices struct{ service *agent.Service }

func (m guestResolutionServices) GetService(id string) *agent.Service {
	if id == "guest-agent" {
		return m.service
	}
	return nil
}
func (m guestResolutionServices) Default() *agent.Service { return m.service }

type guestResolutionStore struct {
	guest sqlc.ChannelGuest
	calls int
}

func (s *guestResolutionStore) ResolveOrCreateGuest(_ context.Context, channelID, platform, externalID string, _ int) (sqlc.ChannelGuest, error) {
	s.calls++
	s.guest.ChannelID, s.guest.Platform, s.guest.ExternalID = channelID, platform, externalID
	return s.guest, nil
}

func TestCoordinatorGuestResolutionIsSupportedPrivateOptInOnly(t *testing.T) {
	ctx := context.Background()
	manager := guestResolutionServices{service: &agent.Service{}}
	channel := config.Channel{ID: "discord-main", Type: "discord", AgentID: "guest-agent", Enabled: true, Config: `{"allow_dm":true,"allow_unlinked_dm":true}`}
	store := &guestResolutionConfigStore{channel: channel}
	authStore := guestResolutionAuthStore{}

	resolve := func(platform string, isGroup bool, guests GuestStore) (*ResolvedChat, error) {
		return ResolveWithChannel(ctx, manager, store, authStore, nil, nil, guests, platform, channel.ID, "discord-user", nil, "Guest", "chat", "", isGroup)
	}
	if _, err := resolve("discord", false, nil); !errors.Is(err, ErrAgentAccessDenied) {
		t.Fatalf("guest routing without store = %v, want access denied", err)
	}

	for _, tc := range []struct {
		name   string
		config string
	}{
		{name: "allow dm disabled", config: `{"allow_dm":false,"allow_unlinked_dm":true}`},
		{name: "unlinked dm disabled", config: `{"allow_dm":true,"allow_unlinked_dm":false}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store.channel.Config = tc.config
			guestStore := &guestResolutionStore{guest: sqlc.ChannelGuest{ID: "guest-1"}}
			if _, err := resolve("discord", false, guestStore); !errors.Is(err, ErrAgentAccessDenied) {
				t.Fatalf("resolve = %v, want access denied", err)
			}
			if guestStore.calls != 0 {
				t.Fatalf("guest store calls = %d", guestStore.calls)
			}
		})
	}
	store.channel = channel
	for _, platform := range []string{"discord", "telegram", "feishu"} {
		t.Run(platform+" private", func(t *testing.T) {
			store.channel.Type = platform
			guestStore := &guestResolutionStore{guest: sqlc.ChannelGuest{ID: "guest-1"}}
			resolved, err := resolve(platform, false, guestStore)
			if err != nil {
				t.Fatal(err)
			}
			if resolved.GuestID != "guest-1" || resolved.AgentID != "guest-agent" || resolved.DedicatedChannelID != channel.ID || guestStore.calls != 1 {
				t.Fatalf("resolved guest = %#v, calls = %d", resolved, guestStore.calls)
			}
		})
	}
	store.channel = channel

	for _, tc := range []struct {
		name     string
		platform string
		isGroup  bool
	}{
		{name: "unsupported private", platform: "qq"},
		{name: "Discord group", platform: "discord", isGroup: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			guestStore := &guestResolutionStore{guest: sqlc.ChannelGuest{ID: "unexpected"}}
			_, _ = resolve(tc.platform, tc.isGroup, guestStore)
			if guestStore.calls != 0 {
				t.Fatalf("guest store calls = %d, want 0", guestStore.calls)
			}
		})
	}
}
