package host_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
	access "github.com/CherryHQ/stella/internal/core/access"
	"github.com/CherryHQ/stella/internal/platform/config"
	pluginhost "github.com/CherryHQ/stella/internal/plugin/host"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

type hostResolverStore struct {
	agent   config.Agent
	channel config.Channel
}

func (s hostResolverStore) GetAgent(_ context.Context, id string) (config.Agent, error) {
	if id != s.agent.ID {
		return config.Agent{}, errors.New("agent not found")
	}
	return s.agent, nil
}

func (s hostResolverStore) ListAgents(context.Context) ([]config.Agent, error) {
	return []config.Agent{s.agent}, nil
}

func (s hostResolverStore) GetChannel(_ context.Context, id string) (config.Channel, error) {
	if id != s.channel.ID {
		return config.Channel{}, errors.New("channel not found")
	}
	return s.channel, nil
}

type noAssignments struct{}

func (noAssignments) ListUserAgentIDs(context.Context, string) ([]string, error) { return nil, nil }

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

func TestGuestAccessUsesHostResolverAndCurrentPersistedConfig(t *testing.T) {
	host := pluginhost.New(nil)
	host.RegisterPluginID("channel/fake")
	host.SetInfo(pkgplugins.PluginInfo{ID: "channel/fake", Kind: "channel", Name: "fake-channel", DisplayName: "Fake Channel"})
	host.AddChannel(pkgplugins.ChannelSpec{PluginID: "channel/fake", Name: "fake-channel", GuestPolicy: fakeGuestPolicy})
	ctx := context.Background()
	guest, err := authz.NewGuestAuthority("guest-1", "channel-1")
	if err != nil {
		t.Fatal(err)
	}
	store := &hostResolverStore{
		agent: config.Agent{ID: "dedicated", Scope: config.AgentScopeRestricted, Enabled: true},
		channel: config.Channel{
			ID: "channel-1", AgentID: "dedicated", Type: "fake-channel", Enabled: true,
			Config: `{"token":"token","allow_dm":true,"allow_unlinked_dm":true}`,
		},
	}
	svc := access.NewService(store, noAssignments{}, access.WithGuestPolicyDecoder(host.GuestPolicyResolver))
	decision, err := svc.Begin(ctx, guest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decision.UseDedicated(ctx, "dedicated", "channel-1"); err != nil {
		t.Fatalf("initial host-resolved guest access = %v", err)
	}
	store.channel.Config = `{"token":"token","allow_dm":true,"allow_unlinked_dm":false}`
	decision, err = svc.Begin(ctx, guest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decision.UseDedicated(ctx, "dedicated", "channel-1"); !errors.Is(err, access.ErrForbidden) {
		t.Fatalf("guest access after persisted config change = %v, want forbidden", err)
	}
}
