package server

import (
	"bytes"
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/internal/agent"
	agentsession "github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/asset"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/authz/policy"
	"github.com/CherryHQ/stella/internal/blob"
	"github.com/CherryHQ/stella/internal/config"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/memorytest"
	"github.com/CherryHQ/stella/internal/sessionaccess"
	storepkg "github.com/CherryHQ/stella/internal/store"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

type startNotifyingRecorder struct {
	*httptest.ResponseRecorder
	start chan struct{}
	once  sync.Once
}

func (w *startNotifyingRecorder) Write(p []byte) (int, error) {
	n, err := w.ResponseRecorder.Write(p)
	if bytes.Contains(p, []byte(`"type":"start"`)) {
		w.once.Do(func() { close(w.start) })
	}
	return n, err
}

func TestStreamAgentEventsIdleDoesNotCallDeliveryGuard(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := make(chan agent.Event)
	rr := &startNotifyingRecorder{ResponseRecorder: httptest.NewRecorder(), start: make(chan struct{})}
	var begins atomic.Int64
	done := make(chan struct{})
	go func() {
		streamAgentEvents(ctx, rr, rr, "a1", "s1", ch, func() error {
			begins.Add(1)
			return nil
		})
		close(done)
	}()
	select {
	case <-rr.start:
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not become idle")
	}
	if got := begins.Load(); got != 0 {
		t.Fatalf("delivery-time Begins while idle=%d, want 0", got)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not stop after cancellation")
	}
}

func TestStreamAgentEventsDenialDoesNotEncodeProtectedSourceEvent(t *testing.T) {
	ch := make(chan agent.Event, 2)
	ch <- agent.Event{Store: ai.UserMessage{Content: "stored"}}
	ch <- agent.Event{Err: errors.New("secret source error")}
	close(ch)

	rr := httptest.NewRecorder()
	guardCalls := 0
	streamAgentEvents(context.Background(), rr, rr, "a1", "s1", ch, func() error {
		guardCalls++
		return errors.New("revoked")
	})

	body := rr.Body.String()
	if guardCalls != 1 {
		t.Fatalf("guard calls=%d, want 1", guardCalls)
	}
	if !strings.Contains(body, `"type":"start"`) {
		t.Fatalf("stream did not write initial start event: %q", body)
	}
	for _, forbidden := range []string{"secret", "text-delta", "errorText", "[DONE]"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("stream encoded %q after denial: %q", forbidden, body)
		}
	}
}

// pausedAuthorizer forces the delivery-time Begin to happen after a concurrent
// policy mutation commits. It uses the real PostgreSQL authorizer underneath,
// so this proves the new revision prevents encoding rather than merely testing
// a fake guard callback.
type pausedAuthorizer struct {
	authz.Authorizer
	mu           sync.Mutex
	begins       int
	secondBegin  chan struct{}
	resumeSecond chan struct{}
}

func (a *pausedAuthorizer) BeginCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.begins
}

func (a *pausedAuthorizer) Begin(ctx context.Context, authority authz.Authority) (authz.Evaluation, error) {
	a.mu.Lock()
	a.begins++
	begin := a.begins
	a.mu.Unlock()
	if begin == 2 {
		close(a.secondBegin)
		<-a.resumeSecond
	}
	return a.Authorizer.Begin(ctx, authority)
}

type streamRuntime struct{ events chan agent.Event }

type streamRuntimeManager struct{ runtime *streamRuntime }

func (m streamRuntimeManager) GetService(string) sessionaccess.RuntimeService { return m.runtime }
func (m streamRuntimeManager) Default() sessionaccess.RuntimeService          { return m.runtime }

func (r *streamRuntime) Chat(context.Context, agent.ChatRequest) <-chan agent.Event {
	panic("Attach must not start a chat")
}

func (r *streamRuntime) SubscribeSession(string) (<-chan agent.Event, func()) {
	return r.events, func() {}
}
func (r *streamRuntime) SessionLive(string) bool { return true }
func (r *streamRuntime) CompactSession(context.Context, agentsession.Info) (string, error) {
	return "", errors.New("Attach must not compact")
}

func TestStreamAgentEventsConcurrentRevocationDoesNotEncodePostDenyEvent(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	store := storepkg.NewDBStore(pool)
	const agentID, sessionID = "stream-agent", "stream-session"
	ownerID := uuid.NewString()
	if err := store.CreateAgent(ctx, config.Agent{ID: agentID, Name: "stream", Model: "test/model", Scope: config.AgentScopeSystem, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	mem := memorytest.New()
	now := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)
	if err := mem.SaveInfo(ctx, memory.SessionInfo{
		ID: sessionID, UserID: ownerID, AgentID: agentID, Channel: string(agentsession.ChannelWeb), Kind: string(agentsession.KindChat), CreatedAt: now, LastActive: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlc.New(pool).CreateConversation(ctx, sqlc.CreateConversationParams{
		ID: uuid.NewString(), SessionID: sessionID, Channel: string(agentsession.ChannelWeb), Kind: string(agentsession.KindChat), LastActive: now,
		AgentID: pgtype.Text{String: agentID, Valid: true}, UserID: pgtype.Text{String: ownerID, Valid: true},
	}); err != nil {
		t.Fatal(err)
	}
	blobStore, err := blob.NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	assets, err := asset.NewStore(t.TempDir(), blobStore, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	inner := policy.New(pool)
	az := &pausedAuthorizer{
		Authorizer: inner, secondBegin: make(chan struct{}), resumeSecond: make(chan struct{}),
	}
	svc, err := sessionaccess.NewService(mem, pool, store, appdb.NewAuthStore(pool), assets, az)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &streamRuntime{events: make(chan agent.Event, 1)}
	if err := svc.BindRuntimeManager(streamRuntimeManager{runtime: runtime}); err != nil {
		t.Fatal(err)
	}
	roles, err := authz.NewRoleSet(authz.RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := authz.NewUserAuthority(authz.UserID(ownerID), roles, authz.GrantSet{})
	if err != nil {
		t.Fatal(err)
	}
	attach, err := svc.Attach(ctx, sessionaccess.AttachInput{Authority: authority, AgentID: agentID, SessionID: sessionID})
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	defer attach.Cancel()

	rr := httptest.NewRecorder()
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	go func() {
		streamAgentEvents(streamCtx, rr, rr, agentID, sessionID, attach.Events, func() error {
			return attach.BeforeProtectedEvent(streamCtx)
		})
		close(done)
	}()

	runtime.events <- agent.Event{Text: "revoked-secret"}
	select {
	case <-az.secondBegin:
	case <-time.After(2 * time.Second):
		t.Fatal("delivery-time Begin did not block")
	}

	revoked := make(chan error, 1)
	go func() {
		_, _, err := policy.NewService(inner).CreatePolicy(ctx, policy.PolicyInput{
			Name:       "revoke stream read",
			Resource:   authz.ResourceSession,
			Action:     authz.ActionRead,
			Effect:     policy.EffectDeny,
			Subjects:   policy.NewSubjectBuilder().Roles(authz.RoleUser).Build(),
			Predicates: []policy.Predicate{policy.Eq("kind", string(agentsession.KindChat))},
		})
		revoked <- err
	}()
	if err := <-revoked; err != nil {
		t.Fatalf("CreatePolicy: %v", err)
	}
	close(az.resumeSecond)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not terminate after delivery-time denial")
	}
	if got := az.BeginCount(); got != 2 {
		t.Fatalf("Begin calls=%d, want initial attach plus one delivery check", got)
	}
	body := rr.Body.String()
	for _, forbidden := range []string{"revoked-secret", "text-delta", "[DONE]"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("stream encoded %q after concurrent revocation: %q", forbidden, body)
		}
	}
}
