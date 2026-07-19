package feishu

import (
	"context"
	"testing"
	"time"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
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
		fetchTenantProfileFn: func(context.Context, string) *TenantProfile {
			return &TenantProfile{UnionID: "on_union1", Name: "Member", Email: "member@example.com"}
		},
	}
	return b
}

func TestAutoProvisionGroupMessagesRequireBotMention(t *testing.T) {
	b := &Bot{}
	if !b.isAutoProvisionMessage("p2p", nil) {
		t.Fatal("direct message should be eligible")
	}
	if b.isAutoProvisionMessage("group", nil) {
		t.Fatal("unmentioned group message should not be eligible")
	}

	b.botOpenID.Store("ou_bot")
	otherID, botID := "ou_other", "ou_bot"
	if b.isAutoProvisionMessage("group", []*larkim.MentionEvent{{Id: &larkim.UserId{OpenId: &otherID}}}) {
		t.Fatal("other-bot mention should not be eligible")
	}
	if !b.isAutoProvisionMessage("group", []*larkim.MentionEvent{{Id: &larkim.UserId{OpenId: &botID}}}) {
		t.Fatal("this-bot mention should be eligible")
	}
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
	fetches := 0
	b.fetchTenantProfileFn = func(context.Context, string) *TenantProfile {
		fetches++
		return &TenantProfile{UnionID: "on_union1"}
	}

	b.maybeAutoProvision(context.Background(), "ou_open1", "on_union1", "t1")
	if len(p.calls) != 0 || fetches != 0 {
		t.Errorf("cache hit: calls=%d fetches=%d, want zero", len(p.calls), fetches)
	}
}

func TestProvisionCacheSweepsExpiredEntries(t *testing.T) {
	b := newProvisionBot(Config{}, &mockProvisioner{})
	now := time.Now()
	b.provisioned["expired"] = now.Add(-provisionCacheTTL)
	b.provisioned["recent"] = now

	if b.isCachedProvision("expired") {
		t.Fatal("expired entry reported as cached")
	}
	if _, ok := b.provisioned["expired"]; ok {
		t.Fatal("expired entry was not removed")
	}
	if !b.isCachedProvision("recent") {
		t.Fatal("recent entry was removed during sweep")
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
	p := &mockProvisioner{}
	b := newProvisionBot(Config{AppID: "a", AppSecret: "s", AutoProvision: true, TenantKey: "t1"}, p)
	b.fetchTenantProfileFn = func(context.Context, string) *TenantProfile { return nil }
	b.maybeAutoProvision(context.Background(), "ou_open1", "on_union1", "t1")
	if len(p.calls) != 0 {
		t.Errorf("nil profile: expected 0 calls, got %d", len(p.calls))
	}
}

func TestMaybeAutoProvisionFailsClosedWithoutEventTenantEvidence(t *testing.T) {
	p := &mockProvisioner{}
	b := newProvisionBot(Config{AppID: "a", AppSecret: "s", AutoProvision: true, TenantKey: "t1"}, p)
	fetches := 0
	b.fetchTenantProfileFn = func(context.Context, string) *TenantProfile {
		fetches++
		return &TenantProfile{UnionID: "on_union1"}
	}
	b.maybeAutoProvision(context.Background(), "ou_open1", "on_event", "")
	b.maybeAutoProvision(context.Background(), "ou_open1", "on_event", "wrong")
	if len(p.calls) != 0 || fetches != 0 {
		t.Fatalf("calls=%d fetches=%d, want zero", len(p.calls), fetches)
	}
}

func TestMaybeAutoProvisionPassesCanonicalProfileAndCachesOnlyAfterSuccess(t *testing.T) {
	p := &mockProvisioner{err: context.DeadlineExceeded}
	b := newProvisionBot(Config{AppID: "a", AppSecret: "s", AutoProvision: true, TenantKey: "t1"}, p)
	b.fetchTenantProfileFn = func(context.Context, string) *TenantProfile {
		return &TenantProfile{UnionID: "on_canonical", Name: "Canonical Member", Email: "canonical@example.com"}
	}

	b.maybeAutoProvision(context.Background(), "ou_open", "on_untrusted_event", "t1")
	b.maybeAutoProvision(context.Background(), "ou_open", "on_untrusted_event", "t1")
	if len(p.calls) != 2 {
		t.Fatalf("failed enrollment calls = %d, want 2 (not cached)", len(p.calls))
	}
	want := pkgchannel.ProvisionRequest{Platform: pkgchannel.PlatformFeishu, ExternalID: "on_canonical", TenantKey: "t1", Email: "canonical@example.com", Name: "Canonical Member"}
	if p.calls[0] != want {
		t.Fatalf("request = %+v, want %+v", p.calls[0], want)
	}

	p.err = nil
	b.maybeAutoProvision(context.Background(), "ou_open", "on_untrusted_event", "t1")
	b.maybeAutoProvision(context.Background(), "ou_open", "on_untrusted_event", "t1")
	if len(p.calls) != 3 {
		t.Fatalf("successful enrollment calls = %d, want 3 (one success then cache)", len(p.calls))
	}
}
