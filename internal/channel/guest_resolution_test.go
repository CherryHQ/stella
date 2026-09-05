package channel

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/platform/config"
	pluginhost "github.com/CherryHQ/stella/internal/plugin/host"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	_ "github.com/CherryHQ/stella/plugins/channels/dingtalk"
	_ "github.com/CherryHQ/stella/plugins/channels/discord"
	_ "github.com/CherryHQ/stella/plugins/channels/feishu"
	_ "github.com/CherryHQ/stella/plugins/channels/telegram"
)

type guestResolutionConfigStore struct {
	config.Store
	channel config.Channel
}

func fakeGuestPolicy(raw string) (pkgchannel.GuestConfig, error) {
	var cfg struct {
		AllowDM         bool `json:"allow_dm"`
		AllowUnlinkedDM bool `json:"allow_unlinked_dm"`
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return pkgchannel.GuestConfig{}, err
	}
	return pkgchannel.GuestConfig{AllowDM: cfg.AllowDM, AllowUnlinkedDM: cfg.AllowUnlinkedDM}, nil
}

func (s *guestResolutionConfigStore) GetChannel(context.Context, string) (config.Channel, error) {
	return s.channel, nil
}

func (s *guestResolutionConfigStore) ListEnabledAgents(context.Context) ([]config.Agent, error) {
	return nil, nil
}

func (s *guestResolutionConfigStore) GetAgent(_ context.Context, id string) (config.Agent, error) {
	if id != s.channel.AgentID {
		return config.Agent{}, pgx.ErrNoRows
	}
	return config.Agent{ID: id, Enabled: true}, nil
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

type guestResolutionGroupResolver struct{}

func (guestResolutionGroupResolver) ResolveGroupID(context.Context, string, string, string) (string, error) {
	return "11111111-1111-4111-8111-111111111111", nil
}

func TestCoordinatorUsesHostResolverForFakeChannelAndCurrentConfig(t *testing.T) {
	host := pluginhost.New(nil)
	host.RegisterPluginID("channel/fake")
	host.SetInfo(pkgplugins.PluginInfo{ID: "channel/fake", Kind: "channel", Name: "fake-channel", DisplayName: "Fake Channel"})
	host.AddChannel(pkgplugins.ChannelSpec{PluginID: "channel/fake", Name: "fake-channel", GuestPolicy: fakeGuestPolicy})

	ctx := context.Background()
	manager := guestResolutionServices{service: &agent.Service{}}
	store := &guestResolutionConfigStore{channel: config.Channel{
		ID: "fake-instance", Type: "fake-channel", AgentID: "guest-agent", Enabled: true,
		Config: `{"allow_dm":true,"allow_unlinked_dm":true}`,
	}}
	authStore := guestResolutionAuthStore{}
	guestStore := &guestResolutionStore{guest: sqlc.ChannelGuest{ID: "guest-1"}}
	resolved, err := ResolveWithChannel(ctx, manager, store, authStore, nil, nil, guestStore, "fake-channel", store.channel.ID, "fake-user", nil, "Guest", "chat", "", false, host.GuestPolicyResolver)
	if err != nil {
		t.Fatalf("initial fake-channel guest resolve = %v", err)
	}
	if resolved.GuestID != "guest-1" || guestStore.calls != 1 {
		t.Fatalf("resolved guest = %#v, calls = %d", resolved, guestStore.calls)
	}

	store.channel.Config = `{"allow_dm":true,"allow_unlinked_dm":false}`
	if _, err := ResolveWithChannel(ctx, manager, store, authStore, nil, nil, &guestResolutionStore{guest: sqlc.ChannelGuest{ID: "guest-1"}}, "fake-channel", store.channel.ID, "fake-user", nil, "Guest", "chat", "", false, host.GuestPolicyResolver); !errors.Is(err, ErrAgentAccessDenied) {
		t.Fatalf("fake-channel resolve after persisted policy change = %v, want access denied", err)
	}
}

func TestCoordinatorGuestResolutionIsSupportedPrivateOptInOnly(t *testing.T) {
	ctx := context.Background()
	resolverHost := pluginhost.New(nil)
	if err := resolverHost.LoadDefaultCatalog(); err != nil {
		t.Fatalf("LoadDefaultCatalog: %v", err)
	}
	guestPolicy := resolverHost.GuestPolicyResolver
	manager := guestResolutionServices{service: &agent.Service{}}
	channel := config.Channel{ID: "discord-main", Type: "discord", AgentID: "guest-agent", Enabled: true, Config: `{"allow_dm":true,"allow_unlinked_dm":true}`}
	store := &guestResolutionConfigStore{channel: channel}
	authStore := guestResolutionAuthStore{}

	resolve := func(platform string, isGroup bool, guests GuestStore) (*ResolvedChat, error) {
		return ResolveWithChannel(ctx, manager, store, authStore, nil, nil, guests, platform, channel.ID, "discord-user", nil, "Guest", "chat", "", isGroup, guestPolicy)
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
	t.Run("persisted config change closes guest admission", func(t *testing.T) {
		if _, err := resolve("discord", false, &guestResolutionStore{guest: sqlc.ChannelGuest{ID: "guest-1"}}); err != nil {
			t.Fatalf("initial resolve = %v", err)
		}
		store.channel.Config = `{"allow_dm":true,"allow_unlinked_dm":false}`
		if _, err := resolve("discord", false, &guestResolutionStore{guest: sqlc.ChannelGuest{ID: "guest-1"}}); !errors.Is(err, ErrAgentAccessDenied) {
			t.Fatalf("resolve after persisted policy change = %v, want access denied", err)
		}
	})
	store.channel = channel
	t.Run("missing canonical sender cannot create guest", func(t *testing.T) {
		guestStore := &guestResolutionStore{guest: sqlc.ChannelGuest{ID: "guest-1"}}
		_, err := ResolveWithChannel(ctx, manager, store, authStore, nil, nil, guestStore, "discord", channel.ID, "", []string{"legacy-id"}, "Guest", "chat", "", false, guestPolicy)
		if !errors.Is(err, ErrAgentAccessDenied) {
			t.Fatalf("resolve = %v, want access denied", err)
		}
		if guestStore.calls != 0 {
			t.Fatalf("guest store calls = %d, want 0", guestStore.calls)
		}
	})
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

	t.Run("unlinked Discord group uses channel binding", func(t *testing.T) {
		store.channel = channel
		guestStore := &guestResolutionStore{guest: sqlc.ChannelGuest{ID: "unexpected"}}
		resolved, err := ResolveWithChannel(ctx, manager, store, authStore, nil, guestResolutionGroupResolver{}, guestStore, "discord", channel.ID, "unlinked-member", nil, "Guest", "guild-channel", "", true, guestPolicy)
		if err != nil {
			t.Fatal(err)
		}
		if resolved.AgentID != channel.AgentID || resolved.GroupID == "" || resolved.User.ID != "" {
			t.Fatalf("resolved group = %#v", resolved)
		}
		if guestStore.calls != 0 {
			t.Fatalf("guest store calls = %d, want 0", guestStore.calls)
		}
	})
}
