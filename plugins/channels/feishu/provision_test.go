package feishu

import (
	"context"
	"testing"
	"time"

	pkgchannel "github.com/vaayne/anna/pkg/channel"
)

// mockProvisioner records ProvisionUser calls.
type mockProvisioner struct {
	mockHandler
	calls []pkgchannel.ProvisionRequest
	err   error
}

func (m *mockProvisioner) ProvisionUser(_ context.Context, req pkgchannel.ProvisionRequest) error {
	m.calls = append(m.calls, req)
	return m.err
}

func newProvisionBot(cfg Config, p *mockProvisioner) *Bot {
	b := &Bot{
		handler:     p,
		cfg:         cfg,
		provisioned: make(map[string]time.Time),
		chatModels:  make(map[string]pkgchannel.ModelOption),
		seenMsgs:    make(map[string]time.Time),
	}
	return b
}

func TestMaybeAutoProvisionDisabled(t *testing.T) {
	p := &mockProvisioner{}
	b := newProvisionBot(Config{AppID: "a", AppSecret: "s", AutoProvision: false, TenantKey: "t1"}, p)
	// With a nil client, fetchTenantProfile would panic — but it should never be
	// reached when AutoProvision is false.
	b.maybeAutoProvision(context.Background(), "ou_open1", "on_union1", "t1")
	if len(p.calls) != 0 {
		t.Errorf("expected 0 calls, got %d", len(p.calls))
	}
}

func TestMaybeAutoProvisionNoTenantKeyKnown(t *testing.T) {
	// Neither cfg.TenantKey nor learnedTenantKey is set — should skip silently.
	p := &mockProvisioner{}
	b := newProvisionBot(Config{AppID: "a", AppSecret: "s", AutoProvision: true, TenantKey: ""}, p)
	b.maybeAutoProvision(context.Background(), "ou_open1", "on_union1", "t1")
	if len(p.calls) != 0 {
		t.Errorf("expected 0 calls, got %d", len(p.calls))
	}
}

func TestMaybeAutoProvisionLearnedTenantKey(t *testing.T) {
	// learnedTenantKey set (simulates startup fetch) — wrong-tenant event skips.
	p := &mockProvisioner{}
	b := newProvisionBot(Config{AppID: "a", AppSecret: "s", AutoProvision: true, TenantKey: ""}, p)
	b.learnedTenantKey = "t1"
	// Wrong tenant in event: should skip.
	b.maybeAutoProvision(context.Background(), "ou_open1", "on_union1", "wrong")
	if len(p.calls) != 0 {
		t.Errorf("wrong tenant with learned key: expected 0 calls, got %d", len(p.calls))
	}
}

func TestMaybeAutoProvisionNoProvisioner(t *testing.T) {
	// handler does not implement Provisioner.
	b := &Bot{
		handler:     &mockHandler{},
		cfg:         Config{AppID: "a", AppSecret: "s", AutoProvision: true, TenantKey: "t1"},
		provisioned: make(map[string]time.Time),
	}
	// Should return silently without panic.
	b.maybeAutoProvision(context.Background(), "ou_open1", "on_union1", "t1")
}

func TestMaybeAutoProvisionCacheHit(t *testing.T) {
	p := &mockProvisioner{}
	b := newProvisionBot(Config{AppID: "a", AppSecret: "s", AutoProvision: true, TenantKey: "t1"}, p)
	// Pre-populate the cache so it looks like this user was recently provisioned.
	b.provisioned["on_union1"] = time.Now()

	b.maybeAutoProvision(context.Background(), "ou_open1", "on_union1", "t1")
	if len(p.calls) != 0 {
		t.Errorf("cache hit: expected 0 calls, got %d", len(p.calls))
	}
}

func TestMaybeAutoProvisionWrongTenantSkips(t *testing.T) {
	p := &mockProvisioner{}
	b := newProvisionBot(Config{AppID: "a", AppSecret: "s", AutoProvision: true, TenantKey: "t1"}, p)
	// Pass a different tenant key — should return before any API call.
	b.maybeAutoProvision(context.Background(), "ou_open1", "on_union1", "wrong_tenant")
	if len(p.calls) != 0 {
		t.Errorf("wrong tenant: expected 0 calls, got %d", len(p.calls))
	}
}

func TestMaybeAutoProvisionNilProfileSkips(t *testing.T) {
	// Bot with a nil lark client — fetchTenantProfile will fail and return nil.
	// maybeAutoProvision should skip silently.
	p := &mockProvisioner{}
	b := newProvisionBot(Config{AppID: "a", AppSecret: "s", AutoProvision: true, TenantKey: "t1"}, p)
	// client is nil → fetchTenantProfile returns nil
	b.maybeAutoProvision(context.Background(), "ou_open1", "on_union1", "t1")
	// No panic, no provision call (profile was nil).
	if len(p.calls) != 0 {
		t.Errorf("nil profile: expected 0 calls, got %d", len(p.calls))
	}
}
