package access

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/internal/agent"
	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
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

type fakeRuntimeManager struct{ svc *fakeRuntimeService }

func (m fakeRuntimeManager) GetService(string) RuntimeService { return m.svc }
func (m fakeRuntimeManager) Default() RuntimeService          { return m.svc }

type fakeRuntimeService struct {
	chatCalls      int
	subscribeCalls int
	live           bool
	events         chan agent.Event
	lastChat       agent.ChatRequest
}

func (s *fakeRuntimeService) Chat(_ context.Context, req agent.ChatRequest) <-chan agent.Event {
	s.chatCalls++
	s.lastChat = req
	ch := make(chan agent.Event, 2)
	ch <- agent.Event{Text: "hello"}
	ch <- agent.Event{Text: " world"}
	close(ch)
	return ch
}

func TestSendForwardsOnlyExplicitPrivateHumanCapability(t *testing.T) {
	svc, rt, _, authority := newRuntimeTestService(t)
	if _, err := svc.Send(context.Background(), SendInput{
		Authority:    authority,
		AgentID:      "a1",
		SessionID:    "s1",
		Message:      "hello",
		PrivateHuman: true,
	}); err != nil {
		t.Fatalf("Send private human: %v", err)
	}
	if len(rt.lastChat.RuntimeOpts) != 1 {
		t.Fatalf("private-human runtime options = %d, want 1", len(rt.lastChat.RuntimeOpts))
	}

	if _, err := svc.Send(context.Background(), SendInput{
		Authority: authority,
		AgentID:   "a1",
		SessionID: "s1",
		Message:   "automation",
	}); err != nil {
		t.Fatalf("Send automation: %v", err)
	}
	if len(rt.lastChat.RuntimeOpts) != 0 {
		t.Fatalf("automation runtime options = %d, want 0", len(rt.lastChat.RuntimeOpts))
	}
}

func (s *fakeRuntimeService) SubscribeSession(string) (<-chan agent.Event, func()) {
	s.subscribeCalls++
	if s.events == nil {
		s.events = make(chan agent.Event)
	}
	return s.events, func() {}
}
func (s *fakeRuntimeService) SessionLive(string) bool { return s.live }
func (s *fakeRuntimeService) CompactAuthorizedSession(context.Context, agentsession.Info) (string, error) {
	return "", errors.New("not used")
}

func TestMain(m *testing.M) { dbtest.Main(m) }

func TestSendStartsOneTurnAndChunksDoNotReevaluate(t *testing.T) {
	svc, rt, _, authority := newRuntimeTestService(t)
	result, err := svc.Send(context.Background(), SendInput{Authority: authority, AgentID: "a1", SessionID: "s1", Message: "hello"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	for range result.Events {
	}
	if rt.chatCalls != 1 || rt.subscribeCalls != 0 {
		t.Fatalf("chat=%d subscribe=%d, want one chat and no subscribe", rt.chatCalls, rt.subscribeCalls)
	}
}

func TestAttachOnlySubscribesAndGuardReChecksAccess(t *testing.T) {
	svc, rt, _, authority := newRuntimeTestService(t)
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
	// Each protected event re-checks durable access; while nothing has changed the
	// guard keeps passing.
	if err := attach.BeforeProtectedEvent(context.Background()); err != nil {
		t.Fatalf("first guard: %v", err)
	}
	if err := attach.BeforeProtectedEvent(context.Background()); err != nil {
		t.Fatalf("second guard: %v", err)
	}
}

func TestAttachIdleSubscribes(t *testing.T) {
	svc, rt, _, authority := newRuntimeTestService(t)
	attach, err := svc.Attach(context.Background(), AttachInput{Authority: authority, AgentID: "a1", SessionID: "s1"})
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if attach.Cancel == nil || rt.events == nil {
		t.Fatal("Attach did not subscribe")
	}
}

// TestAttachGuardDeniesAfterDurableAgentRevocation proves the guard re-reads
// durable session access on every protected event: once the session's agent is
// deleted, the guard denies without any custom policy mutation.
func TestAttachGuardDeniesAfterDurableAgentRevocation(t *testing.T) {
	svc, _, store, authority := newRuntimeTestService(t)
	attach, err := svc.Attach(context.Background(), AttachInput{Authority: authority, AgentID: "a1", SessionID: "s1"})
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if err := attach.BeforeProtectedEvent(context.Background()); err != nil {
		t.Fatalf("guard before revocation: %v", err)
	}
	if err := store.DeleteAgent(context.Background(), "a1"); err != nil {
		t.Fatalf("DeleteAgent: %v", err)
	}
	if err := attach.BeforeProtectedEvent(context.Background()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("guard after revocation = %v, want ErrNotFound", err)
	}
}

// TestSendRejectsArchivedSessionDistinguishably covers a client that kept sending
// to a session `/new` rotated away: the refusal must carry ErrArchived so the
// transport can tell "rotated away" apart from "gone", and no turn may start.
func TestSendRejectsArchivedSessionDistinguishably(t *testing.T) {
	ctx := context.Background()
	svc, rt, _, authority := newRuntimeTestService(t)
	info, err := svc.memory.LoadInfo(ctx, "s1")
	if err != nil {
		t.Fatalf("LoadInfo: %v", err)
	}
	info.Archived = true
	if err := svc.memory.SaveInfo(ctx, info); err != nil {
		t.Fatalf("SaveInfo: %v", err)
	}

	_, err = svc.Send(ctx, SendInput{Authority: authority, AgentID: "a1", SessionID: "s1", Message: "hello"})
	if !errors.Is(err, agentsession.ErrArchived) {
		t.Fatalf("Send = %v, want ErrArchived", err)
	}
	if rt.chatCalls != 0 {
		t.Fatalf("chat=%d, want no turn started on an archived session", rt.chatCalls)
	}
}

func newRuntimeTestService(t *testing.T) (*Service, *fakeRuntimeService, config.Store, authz.Authority) {
	t.Helper()
	ctx := context.Background()
	owner := uuid.NewString()
	mem := memorytest.New()
	now := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)
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
	assets, err := asset.NewStore(t.TempDir(), blobStore, nil)
	if err != nil {
		t.Fatalf("asset.NewStore: %v", err)
	}
	agentAccess := agentaccess.NewService(store, appdb.NewAuthStore(db))
	svc, err := NewService(mem, db, store, assets, agentAccess)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	rt := &fakeRuntimeService{}
	if err := svc.BindRuntimeManager(fakeRuntimeManager{svc: rt}); err != nil {
		t.Fatalf("BindRuntimeManager: %v", err)
	}
	authority, err := (auth.Subject{UserID: owner, Roles: []string{auth.RoleUser}}).Authority()
	if err != nil {
		t.Fatalf("Authority: %v", err)
	}
	return svc, rt, store, authority
}
