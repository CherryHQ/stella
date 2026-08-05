package server

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/internal/agent"
	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	agentsession "github.com/CherryHQ/stella/internal/agent/session"
	sessionaccess "github.com/CherryHQ/stella/internal/agent/session/access"
	"github.com/CherryHQ/stella/internal/asset"
	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/blob"
	"github.com/CherryHQ/stella/internal/config"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/memorytest"
	cfgstore "github.com/CherryHQ/stella/internal/store"
	sqlc "github.com/CherryHQ/stella/pkg/db/sqlc"
)

// TestStreamAgentEventsGuardHidesProtectedEventOnRevocation proves the Attach
// stream re-authorizes at delivery time: when the per-event guard denies (the
// shape BeforeProtectedEvent returns after durable session access is revoked),
// the protected source event is never encoded onto the wire.
func TestStreamAgentEventsGuardHidesProtectedEventOnRevocation(t *testing.T) {
	ch := make(chan agent.Event, 1)
	ch <- agent.Event{Text: "secret-after-revocation"}
	close(ch)

	rr := httptest.NewRecorder()
	guard := func() error { return sessionaccess.ErrNotFound }
	streamAgentEvents(context.Background(), rr, rr, "a1", "s1", ch, guard)

	body := rr.Body.String()
	if strings.Contains(body, "secret-after-revocation") {
		t.Fatalf("protected event leaked after revocation: %q", body)
	}
	if strings.Contains(body, "text-delta") {
		t.Fatalf("encoded a text delta after revocation: %q", body)
	}
}

// TestStreamAgentEventsGuardAllowsProtectedEvent is the positive control: while
// the guard passes, the protected event is encoded normally.
func TestStreamAgentEventsGuardAllowsProtectedEvent(t *testing.T) {
	ch := make(chan agent.Event, 1)
	ch <- agent.Event{Text: "visible"}
	close(ch)

	rr := httptest.NewRecorder()
	guard := func() error { return nil }
	streamAgentEvents(context.Background(), rr, rr, "a1", "s1", ch, guard)

	if body := rr.Body.String(); !strings.Contains(body, "visible") {
		t.Fatalf("protected event not encoded while allowed: %q", body)
	}
}

type stubRuntimeManager struct{ svc *stubRuntimeService }

func (m stubRuntimeManager) GetService(string) sessionaccess.RuntimeService { return m.svc }
func (m stubRuntimeManager) Default() sessionaccess.RuntimeService          { return m.svc }

type stubRuntimeService struct{ events chan agent.Event }

func (s *stubRuntimeService) Chat(context.Context, agent.ChatRequest) <-chan agent.Event { return nil }
func (s *stubRuntimeService) StopSession(context.Context, string) bool                   { return false }
func (s *stubRuntimeService) SubscribeSession(string) (<-chan agent.Event, func()) {
	return s.events, func() {}
}
func (s *stubRuntimeService) SessionLive(string) bool { return true }
func (s *stubRuntimeService) CompactAuthorizedSession(context.Context, agentsession.Info) (string, error) {
	return "", nil
}

// TestStreamSessionEventsHidesEventsAfterDurableRevocation is the integrated
// proof: a real Attach guard, re-checking durable session access on every event,
// stops the stream from encoding a protected event once the session's agent is
// durably deleted. No custom policy mutation is involved.
func TestStreamSessionEventsHidesEventsAfterDurableRevocation(t *testing.T) {
	ctx := context.Background()
	owner := uuid.NewString()
	now := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)

	mem := memorytest.New()
	if err := mem.SaveInfo(ctx, memory.SessionInfo{ID: "s1", UserID: owner, AgentID: "a1", Kind: string(agentsession.KindChat), Channel: string(agentsession.ChannelWeb), CreatedAt: now, LastActive: now}); err != nil {
		t.Fatalf("SaveInfo: %v", err)
	}
	db := dbtest.New(t)
	store := cfgstore.NewDBStore(db)
	if err := store.CreateAgent(ctx, config.Agent{ID: "a1", Scope: config.AgentScopeSystem, Enabled: true}); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	if _, err := sqlc.New(db).CreateConversation(ctx, sqlc.CreateConversationParams{
		ID: uuid.NewString(), SessionID: "s1", UserID: pgtype.Text{String: owner, Valid: true}, AgentID: pgtype.Text{String: "a1", Valid: true}, Channel: string(agentsession.ChannelWeb), Kind: string(agentsession.KindChat), LastActive: now,
	}); err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	blobStore, err := blob.NewFSStore(t.TempDir())
	if err != nil {
		t.Fatalf("blob.NewFSStore: %v", err)
	}
	assets, err := asset.NewStore(t.TempDir(), blobStore)
	if err != nil {
		t.Fatalf("asset.NewStore: %v", err)
	}
	svc, err := sessionaccess.NewService(mem, db, store, assets.SessionMedia(), agentaccess.NewService(store, appdb.NewAuthStore(db)))
	if err != nil {
		t.Fatalf("sessionaccess.NewService: %v", err)
	}
	rt := &stubRuntimeService{events: make(chan agent.Event, 1)}
	if err := svc.BindRuntimeManager(stubRuntimeManager{svc: rt}); err != nil {
		t.Fatalf("BindRuntimeManager: %v", err)
	}
	authority, err := (auth.Subject{UserID: owner, Roles: []string{auth.RoleUser}}).Authority()
	if err != nil {
		t.Fatalf("Authority: %v", err)
	}

	attach, err := svc.Attach(ctx, sessionaccess.AttachInput{Authority: authority, AgentID: "a1", SessionID: "s1"})
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if err := attach.BeforeProtectedEvent(ctx); err != nil {
		t.Fatalf("guard before revocation: %v", err)
	}

	// Durable revocation: the session's agent no longer exists.
	if err := store.DeleteAgent(ctx, "a1"); err != nil {
		t.Fatalf("DeleteAgent: %v", err)
	}

	rt.events <- agent.Event{Text: "secret-after-revocation"}
	close(rt.events)
	rr := httptest.NewRecorder()
	streamAgentEvents(ctx, rr, rr, "a1", "s1", attach.Events, func() error {
		return attach.BeforeProtectedEvent(ctx)
	})
	if body := rr.Body.String(); strings.Contains(body, "secret-after-revocation") {
		t.Fatalf("protected event leaked after durable revocation: %q", body)
	}
}
