package access

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/internal/agent"
	agentsession "github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/asset"
	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/blob"
	"github.com/CherryHQ/stella/internal/config"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/memorytest"
	cfgstore "github.com/CherryHQ/stella/internal/store"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

type countedAuthorizer struct {
	begins int
	denyAt map[int]bool
}

func (a *countedAuthorizer) Begin(context.Context, authz.Authority) (authz.Evaluation, error) {
	a.begins++
	return countedEvaluation{allow: !a.denyAt[a.begins]}, nil
}

type countedEvaluation struct{ allow bool }

func (e countedEvaluation) Decide(authz.Request) (authz.Decision, error) {
	if e.allow {
		return authz.Allow("runtime-test", authz.AuditRecord{}), nil
	}
	return authz.Deny(authz.VisibilityHidden, "runtime-test", authz.AuditRecord{}), nil
}
func (countedEvaluation) Revision() int64 { return 1 }

type fakeRuntimeManager struct{ svc *fakeRuntimeService }

func (m fakeRuntimeManager) GetService(string) RuntimeService { return m.svc }
func (m fakeRuntimeManager) Default() RuntimeService          { return m.svc }

type fakeRuntimeService struct {
	chatCalls      int
	subscribeCalls int
	live           bool
	events         chan agent.Event
}

func (s *fakeRuntimeService) Chat(context.Context, agent.ChatRequest) <-chan agent.Event {
	s.chatCalls++
	ch := make(chan agent.Event, 2)
	ch <- agent.Event{Text: "hello"}
	ch <- agent.Event{Text: " world"}
	close(ch)
	return ch
}

func (s *fakeRuntimeService) SubscribeSession(string) (<-chan agent.Event, func()) {
	s.subscribeCalls++
	if s.events == nil {
		s.events = make(chan agent.Event)
	}
	return s.events, func() {}
}
func (s *fakeRuntimeService) SessionLive(string) bool { return s.live }
func (s *fakeRuntimeService) CompactSession(context.Context, agentsession.Info) (string, error) {
	return "", errors.New("not used")
}

func TestMain(m *testing.M) { dbtest.Main(m) }

func TestSendStartsOneAccessAndChunksDoNotReevaluate(t *testing.T) {
	svc, az, rt, authority := newRuntimeTestService(t, nil)
	result, err := svc.Send(context.Background(), SendInput{Authority: authority, AgentID: "a1", SessionID: "s1", Message: "hello"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	for range result.Events {
	}
	if az.begins != 1 {
		t.Fatalf("Begin calls=%d, want 1", az.begins)
	}
	if rt.chatCalls != 1 || rt.subscribeCalls != 0 {
		t.Fatalf("chat=%d subscribe=%d, want one chat and no subscribe", rt.chatCalls, rt.subscribeCalls)
	}
}

func TestAttachOnlySubscribesAndGuardBeginsPerProtectedEvent(t *testing.T) {
	svc, az, rt, authority := newRuntimeTestService(t, nil)
	rt.live = true
	attach, err := svc.Attach(context.Background(), AttachInput{Authority: authority, AgentID: "a1", SessionID: "s1"})
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if !attach.Live {
		t.Fatal("Attach Live=false, want true")
	}
	if rt.chatCalls != 0 || rt.subscribeCalls != 1 {
		t.Fatalf("chat=%d subscribe=%d, want no chat and one subscribe", rt.chatCalls, rt.subscribeCalls)
	}
	if az.begins != 1 {
		t.Fatalf("initial Begin calls=%d, want 1", az.begins)
	}
	if err := attach.BeforeProtectedEvent(context.Background()); err != nil {
		t.Fatalf("first guard: %v", err)
	}
	if err := attach.BeforeProtectedEvent(context.Background()); err != nil {
		t.Fatalf("second guard: %v", err)
	}
	if az.begins != 3 {
		t.Fatalf("Begin calls=%d, want 3", az.begins)
	}
}

func TestAttachIdleDoesNotBeginAgain(t *testing.T) {
	svc, az, rt, authority := newRuntimeTestService(t, nil)
	attach, err := svc.Attach(context.Background(), AttachInput{Authority: authority, AgentID: "a1", SessionID: "s1"})
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if attach.Cancel == nil || rt.events == nil {
		t.Fatal("Attach did not subscribe")
	}
	if got := az.begins; got != 1 {
		t.Fatalf("Begin calls while idle=%d, want 1", got)
	}
}

func TestAttachGuardRevocationDeniesBeforeDelivery(t *testing.T) {
	svc, az, _, authority := newRuntimeTestService(t, map[int]bool{2: true})
	attach, err := svc.Attach(context.Background(), AttachInput{Authority: authority, AgentID: "a1", SessionID: "s1"})
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if err := attach.BeforeProtectedEvent(context.Background()); err == nil {
		t.Fatal("guard succeeded after revocation, want denial")
	}
	if az.begins != 2 {
		t.Fatalf("Begin calls=%d, want 2", az.begins)
	}
}

func TestAttachGuardKeepsInFlightSnapshotThenDeniesNextEvent(t *testing.T) {
	// Begin #2 represents an event whose evaluation started before revocation;
	// it may complete on that immutable snapshot. Begin #3 represents the next
	// protected event and must observe the committed revocation.
	svc, az, _, authority := newRuntimeTestService(t, map[int]bool{3: true})
	attach, err := svc.Attach(context.Background(), AttachInput{Authority: authority, AgentID: "a1", SessionID: "s1"})
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if err := attach.BeforeProtectedEvent(context.Background()); err != nil {
		t.Fatalf("in-flight event was denied: %v", err)
	}
	if err := attach.BeforeProtectedEvent(context.Background()); err == nil {
		t.Fatal("next event succeeded after revocation, want denial")
	}
	if az.begins != 3 {
		t.Fatalf("Begin calls=%d, want attach plus two events", az.begins)
	}
}

func newRuntimeTestService(t *testing.T, denyAt map[int]bool) (*Service, *countedAuthorizer, *fakeRuntimeService, authz.Authority) {
	t.Helper()
	ctx := context.Background()
	mem := memorytest.New()
	now := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)
	if err := mem.SaveInfo(ctx, memory.SessionInfo{ID: "s1", UserID: "u1", AgentID: "a1", Kind: string(agentsession.KindChat), Channel: string(agentsession.ChannelWeb), CreatedAt: now, LastActive: now}); err != nil {
		t.Fatalf("SaveInfo: %v", err)
	}
	db := dbtest.New(t)
	store := cfgstore.NewDBStore(db)
	if err := store.CreateAgent(ctx, config.Agent{ID: "a1", Scope: config.AgentScopeSystem, Enabled: true}); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	if _, err := sqlc.New(db).CreateConversation(ctx, sqlc.CreateConversationParams{
		ID: uuid.NewString(), SessionID: "s1", UserID: pgtype.Text{String: "u1", Valid: true}, AgentID: pgtype.Text{String: "a1", Valid: true}, Channel: string(agentsession.ChannelWeb), Kind: string(agentsession.KindChat), LastActive: now,
	}); err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	blobStore, err := blob.NewFSStore(t.TempDir())
	if err != nil {
		t.Fatalf("blob.NewFSStore: %v", err)
	}
	assets, err := asset.NewStore(t.TempDir(), blobStore, nil)
	if err != nil {
		t.Fatalf("asset.NewStore: %v", err)
	}
	az := &countedAuthorizer{denyAt: denyAt}
	svc, err := NewService(mem, db, store, appdb.NewAuthStore(db), assets, az)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	rt := &fakeRuntimeService{}
	if err := svc.BindRuntimeManager(fakeRuntimeManager{svc: rt}); err != nil {
		t.Fatalf("BindRuntimeManager: %v", err)
	}
	authority, err := (auth.Subject{UserID: "u1", Roles: []string{auth.RoleUser}}).Authority()
	if err != nil {
		t.Fatalf("Authority: %v", err)
	}
	return svc, az, rt, authority
}
