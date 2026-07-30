package sessionctl

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/agent"
	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	"github.com/CherryHQ/stella/internal/agent/agentctx"
	agentruntime "github.com/CherryHQ/stella/internal/agent/runtime"
	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/memorytest"
	"github.com/CherryHQ/stella/pkg/tools"
)

const (
	testAgentID = "sessionctl-agent"
	testUserID  = "11111111-1111-4111-8111-111111111111"
	testGroupID = "22222222-2222-4222-8222-222222222222"
)

// --- fake nonce store -------------------------------------------------------

// fakeNonceStore is the durable store's contract without a database: Claim is
// the single-use gate and must not hand the same nonce to two callers.
type fakeNonceStore struct {
	mu     sync.Mutex
	nonces map[string]Nonce
}

func newFakeNonceStore() *fakeNonceStore {
	return &fakeNonceStore{nonces: map[string]Nonce{}}
}

func (s *fakeNonceStore) Create(_ context.Context, n Nonce) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nonces[n.ID] = n
	return nil
}

func (s *fakeNonceStore) Get(_ context.Context, id string) (Nonce, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.nonces[id]
	if !ok {
		return Nonce{}, ErrNonceNotFound
	}
	return n, nil
}

func (s *fakeNonceStore) Claim(_ context.Context, id string) (Nonce, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.nonces[id]
	if !ok || n.Used() || n.Expired(time.Now().UTC()) {
		return Nonce{}, ErrNonceNotFound
	}
	n.UsedAt = time.Now().UTC()
	s.nonces[id] = n
	return n, nil
}

// --- agent service fixture --------------------------------------------------

// testServices is a single-agent ServiceManager over a real session Registry, so
// rotation exercises the production compare-and-rotate semantics.
type testServices struct{ svc *agent.Service }

func (m testServices) GetService(agentID string) *agent.Service {
	if agentID != testAgentID {
		return nil
	}
	return m.svc
}

func (m testServices) Default() *agent.Service { return m.svc }

// registryAccess is the Session access port backed directly by the Registry.
// Policy is not under test here; the rotation and resolution semantics are.
type registryAccess struct{ reg *session.Registry }

func (a registryAccess) Begin(context.Context, authz.Authority) (agent.SessionAccess, error) {
	return a, nil
}

func (a registryAccess) Create(ctx context.Context, userID, agentID, projectID string, kind session.Kind, channel session.Channel) (session.Info, error) {
	return a.reg.Ensure(ctx, session.Request{UserID: userID, AgentID: agentID, ProjectID: projectID, Kind: kind, Channel: channel, CreateIfMissing: true})
}

func (a registryAccess) ResolveMain(ctx context.Context, userID, agentID string) (session.Info, error) {
	return a.reg.ResolveMain(ctx, session.MainRequest{UserID: userID, AgentID: agentID})
}

func (a registryAccess) RotateMain(ctx context.Context, userID, agentID, expectedSessionID string) (session.Info, error) {
	return a.reg.RotateMain(ctx, session.MainRequest{UserID: userID, AgentID: agentID, ExpectedSessionID: expectedSessionID})
}

func (a registryAccess) ResolveChatChannel(ctx context.Context, req session.ChannelRequest) (session.Info, error) {
	return a.reg.ResolveChatChannel(ctx, req)
}

func (a registryAccess) RotateChannel(ctx context.Context, req session.ChannelRequest) (session.Info, error) {
	return a.reg.RotateChannel(ctx, req)
}

func (a registryAccess) Use(ctx context.Context, agentID, sessionID string) (session.Info, error) {
	return a.reg.Get(ctx, session.Scope{AgentID: agentID, System: true}, sessionID)
}

func (a registryAccess) EnsureRead(ctx context.Context, req session.Request) (session.Info, error) {
	return a.reg.Ensure(ctx, req)
}

func (a registryAccess) EnsureUse(ctx context.Context, req session.Request) (session.Info, error) {
	return a.reg.Ensure(ctx, req)
}

func (a registryAccess) Delete(ctx context.Context, agentID, sessionID string) (session.Info, error) {
	return a.reg.Get(ctx, session.Scope{AgentID: agentID, System: true}, sessionID)
}

func (a registryAccess) Archive(ctx context.Context, info session.Info) error {
	return a.reg.Archive(ctx, session.Scope{UserID: info.UserID, AgentID: info.AgentID}, info.ID)
}

type fixture struct {
	tool  tools.Tool
	svc   *agent.Service
	store *fakeNonceStore
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	mem := memorytest.New()
	reg, err := session.NewRegistry(mem, testAgentID)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	rt, err := agentruntime.New(agentruntime.Config{
		Memory:    mem,
		NewRunner: func(context.Context, agentruntime.RunnerParams) (agentruntime.Runner, error) { return nil, nil },
	})
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}
	svc := &agent.Service{Sessions: reg, Runtime: rt, SessionAccess: registryAccess{reg: reg}, AgentID: testAgentID}
	store := newFakeNonceStore()
	return &fixture{tool: BuildTool(testServices{svc: svc}, store), svc: svc, store: store}
}

// --- turn contexts ----------------------------------------------------------

// dmTurn builds the context a channel DM turn hands its tools: main-session
// binding, the owner's identity, and a fresh per-turn id.
func (f *fixture) dmTurn(t *testing.T, turnID string) (context.Context, session.Info) {
	t.Helper()
	authority := mustUserAuthority(t)
	info, err := f.svc.ResolveMainSession(context.Background(), authority, testUserID, testAgentID)
	if err != nil {
		t.Fatalf("resolve main: %v", err)
	}
	return dmTurnCtx(info.ID, turnID), info
}

func dmTurnCtx(sessionID, turnID string) context.Context {
	ctx := authz.WithUserID(context.Background(), testUserID)
	ctx = authz.WithAgentID(ctx, testAgentID)
	ctx = memory.WithSessionID(ctx, sessionID)
	ctx = agentctx.WithTurnID(ctx, turnID)
	return agentctx.WithChatBinding(ctx, agentctx.ChatBinding{
		Main:       true,
		Channel:    testAgentID + ":user:" + testUserID + ":private",
		SessionKey: testAgentID + ":user:" + testUserID + ":private",
	})
}

// groupTurn builds the context a group turn hands its tools: the group owns the
// session, the speaker is a per-turn personalization target, and the event-log
// seq is the turn marker.
func (f *fixture) groupTurn(t *testing.T, speakerID string, seq int64) (context.Context, session.Info) {
	t.Helper()
	info, err := f.svc.ResolveChatChannelSession(context.Background(), groupChannelRequest(t))
	if err != nil {
		t.Fatalf("resolve group session: %v", err)
	}
	return groupTurnCtx(info.ID, speakerID, seq), info
}

func groupTurnCtx(sessionID, speakerID string, seq int64) context.Context {
	ctx := authz.WithGroupID(context.Background(), testGroupID)
	ctx = authz.WithAgentID(ctx, testAgentID)
	ctx = memory.WithSessionID(ctx, sessionID)
	ctx = memory.WithGroupSeq(ctx, seq)
	ctx = memory.WithCurrentSpeaker(ctx, memory.CurrentSpeaker{Platform: "telegram", PlatformUserID: speakerID})
	return agentctx.WithChatBinding(ctx, agentctx.ChatBinding{
		Channel:    "group:" + testGroupID,
		SessionKey: agent.BuildGroupSessionKey(testAgentID, testGroupID),
	})
}

func groupChannelRequest(t *testing.T) agent.ChatChannelRequest {
	t.Helper()
	authority := mustGroupAuthority(t)
	return agent.ChatChannelRequest{
		Authority:  authority,
		UserID:     testGroupID,
		GroupID:    testGroupID,
		AgentID:    testAgentID,
		Channel:    session.Channel("group:" + testGroupID),
		SessionKey: agent.BuildGroupSessionKey(testAgentID, testGroupID),
	}
}

func newTurnID() string { return uuid.Must(uuid.NewV7()).String() }

func mustUserAuthority(t *testing.T) authz.Authority {
	t.Helper()
	authority, err := agentaccess.WorkerAgentAuthority(testUserID, testAgentID)
	if err != nil {
		t.Fatalf("worker authority: %v", err)
	}
	return authority
}

func mustGroupAuthority(t *testing.T) authz.Authority {
	t.Helper()
	authority, err := agentaccess.GroupAgentAuthority(testGroupID, testAgentID)
	if err != nil {
		t.Fatalf("group authority: %v", err)
	}
	return authority
}
