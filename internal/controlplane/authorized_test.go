package controlplane

import (
	"context"
	"errors"
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/pluginhost"
	"github.com/CherryHQ/stella/internal/webhook"
)

func newService() *Service {
	// The authorization matrix exercises only the admin gate at Begin, so the
	// persistence/runtime handles are intentionally nil: a denied use case returns
	// at Begin before touching them.
	return NewService(nil, nil, nil, nil, nil)
}

func adminAuthority(t *testing.T) authz.Authority {
	t.Helper()
	a, err := authz.NewUserAuthority(authz.UserID("admin-1"), true)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func userAuthority(t *testing.T) authz.Authority {
	t.Helper()
	a, err := authz.NewUserAuthority(authz.UserID("user-1"), false)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// TestAdminMintsAccess proves an admin authority opens a control-plane Access —
// the sole gate for every provider/settings/plugin/channel operation.
func TestAdminMintsAccess(t *testing.T) {
	acc, err := newService().Begin(context.Background(), adminAuthority(t))
	if err != nil || acc == nil {
		t.Fatalf("admin Begin = (%v, %v), want an Access", acc, err)
	}
}

// TestNonAdminDenied proves a non-admin UserActor is default-denied at Begin —
// the exact contract the legacy requireAdmin gate enforced. No Access is minted,
// so no durable read or external action can run.
func TestNonAdminDenied(t *testing.T) {
	acc, err := newService().Begin(context.Background(), userAuthority(t))
	if !errors.Is(err, authz.ErrForbidden) || acc != nil {
		t.Fatalf("non-admin Begin = (%v, %v), want forbidden and no Access", acc, err)
	}
}

func TestChannelCreateConflictIsStable(t *testing.T) {
	conflict := &ConflictError{}
	if !errors.As(channelCreateError(config.ErrChannelExists), &conflict) || conflict.Msg != "channel already exists" {
		t.Fatalf("channel create conflict = %#v", conflict)
	}
}

type channelSaveStore struct {
	config.Store
	channel config.Channel
	update  config.ChannelUpdate
}

func (s *channelSaveStore) ListChannels(context.Context) ([]config.Channel, error) {
	return nil, nil
}

func (s *channelSaveStore) UpdateChannel(_ context.Context, update config.ChannelUpdate) error {
	s.update = update
	s.channel = update.Channel
	return nil
}

func (s *channelSaveStore) GetChannel(context.Context, string) (config.Channel, error) {
	return s.channel, nil
}

func (s *channelSaveStore) UpsertPlugin(context.Context, config.Plugin) error { return nil }

func (s *channelSaveStore) GetPlugin(_ context.Context, id string) (config.Plugin, error) {
	return config.Plugin{ID: id, Enabled: true, Config: map[string]any{}}, nil
}

func TestSaveOrdinaryChannelDoesNotRequireWebhookService(t *testing.T) {
	ctx := context.Background()
	store := &channelSaveStore{}
	svc := NewService(store, pluginhost.New(store), nil, nil, nil)
	access, err := svc.Begin(ctx, adminAuthority(t))
	if err != nil {
		t.Fatal(err)
	}
	want := config.Channel{ID: "telegram", Type: "telegram", Name: "Telegram", Enabled: true, Config: `{}`}
	got, err := access.SaveChannel(ctx, want, map[string]any{}, false)
	if err != nil {
		t.Fatalf("SaveChannel without webhook service: %v", err)
	}
	if got != want || store.update.Channel != want || store.update.EndpointProvider != "" {
		t.Fatalf("saved = %+v, update = %+v, want %+v", got, store.update, want)
	}
}

func TestSaveWebhookChannelPassesPluginDecodedEndpointProvider(t *testing.T) {
	ctx := context.Background()
	store := &channelSaveStore{}
	svc := NewService(store, pluginhost.New(store), nil, nil, nil)
	access, err := svc.Begin(ctx, adminAuthority(t))
	if err != nil {
		t.Fatal(err)
	}
	ch := config.Channel{ID: "hook", Type: "webhook", AgentID: "agent-1", Enabled: true}
	got, err := access.SaveChannel(ctx, ch, map[string]any{"provider": "generic"}, false)
	if err != nil {
		t.Fatalf("SaveChannel webhook: %v", err)
	}
	if store.update.EndpointProvider != "generic" {
		t.Fatalf("endpoint provider = %q, want generic", store.update.EndpointProvider)
	}
	if got.Config != `{"provider":"generic"}` {
		t.Fatalf("saved config = %q", got.Config)
	}
}

func TestEndpointErrorDistinguishesConfigurationRetryFromEndpointRevocation(t *testing.T) {
	configChanged := &ConflictError{}
	ok := errors.As(endpointError(webhook.ErrChannelConfigChanged), &configChanged)
	if !ok {
		t.Fatalf("config-changed error = %T, want *ConflictError", endpointError(webhook.ErrChannelConfigChanged))
	}
	if configChanged.Msg != "channel configuration changed; retry endpoint issuance" {
		t.Fatalf("config-changed message = %q", configChanged.Msg)
	}

	active := &ConflictError{}
	ok = errors.As(channelSaveError(config.ErrChannelEndpointActive), &active)
	if !ok {
		t.Fatalf("endpoint-active error = %T, want *ConflictError", channelSaveError(config.ErrChannelEndpointActive))
	}
	if active.Msg != "webhook endpoint is active; revoke it before changing the channel binding" {
		t.Fatalf("endpoint-active message = %q", active.Msg)
	}
}

// TestBeginFailsClosed locks the fail-closed properties: a nil service denies with
// ErrUnavailable, and an invalid Authority is forbidden before any work.
func TestBeginFailsClosed(t *testing.T) {
	ctx := context.Background()

	var nilSvc *Service
	if _, err := nilSvc.Begin(ctx, adminAuthority(t)); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("nil service Begin err=%v, want ErrUnavailable", err)
	}

	if _, err := newService().Begin(ctx, authz.Authority{}); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("invalid authority Begin err=%v, want forbidden", err)
	}
}
