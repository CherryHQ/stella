package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	apiserver "github.com/CherryHQ/stella/api/server"
	apitypes "github.com/CherryHQ/stella/api/types"
	cfgstore "github.com/CherryHQ/stella/cmd/stellad/store"
	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/channel"
	agentaccess "github.com/CherryHQ/stella/internal/core/access"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// fakeGroupRunner counts wake signals from fresh ingests.
type fakeGroupRunner struct {
	calls        int
	abortGroupID string
	abortAgentID string
}

func (f *fakeGroupRunner) Wake() {
	f.calls++
}

func (f *fakeGroupRunner) AbortGroupTurn(groupID, agentID string) bool {
	f.abortGroupID = groupID
	f.abortAgentID = agentID
	return true
}

// setupGroupSSE builds a minimal Server whose group boundary has a real event log
// (so append/dedup are exercised) and a fake dispatch runner (so no agent turn is
// needed), plus one group owned by the returned user.
func setupGroupSSE(t *testing.T) (s *Server, runner *fakeGroupRunner, userID, groupID string) {
	t.Helper()
	s, runner, userID, groupID, _, _, _ = setupGroupSSEWithDB(t)
	return s, runner, userID, groupID
}

// setupGroupSSEWithDB is setupGroupSSE plus the handles a test needs to write
// dispatch rows directly: the pool, the member agent id, and the event hub.
func setupGroupSSEWithDB(t *testing.T) (s *Server, runner *fakeGroupRunner, userID, groupID string, db *pgxpool.Pool, agentID string, hub *channel.GroupEventHub) {
	t.Helper()
	db = dbtest.New(t)
	store := cfgstore.NewDBStore(db)
	ctx := t.Context()
	if err := store.Seed(ctx); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	as := appdb.NewAuthStore(db)
	oidc := appdb.NewOIDCStore(db)
	agentAccess := agentaccess.NewService(store, as)
	runner = &fakeGroupRunner{}
	hub = channel.NewGroupEventHub()
	groupSvc := channel.NewGroupService(db, agentAccess, channel.NewRuntimeResolver(store), eventlog.NewStore(db), runner, channel.WithGroupEventHub(hub))

	user, err := oidc.CreateUser(ctx, auth.User{ID: uuid.NewString(), Email: "u@example.com", Name: "u"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	agents, err := store.ListAgents(ctx)
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	var stella string
	for _, a := range agents {
		if a.Name == "Stella" {
			stella = a.ID
		}
	}
	authority, err := authz.NewUserAuthority(authz.UserID(user.ID), false)
	if err != nil {
		t.Fatalf("NewUserAuthority: %v", err)
	}
	acc, err := groupSvc.Begin(ctx, authority)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	g, err := acc.Create(ctx, "team", []string{stella})
	if err != nil {
		t.Fatalf("Create group: %v", err)
	}

	s = &Server{
		groupSvc:   groupSvc,
		log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		runtimeCtx: t.Context(),
	}
	return s, runner, user.ID, g.ID, db, stella, hub
}

func sendGroupSSE(t *testing.T, s *Server, userID, groupID, content, clientID string) *httptest.ResponseRecorder {
	t.Helper()
	body := apitypes.SendGroupMessageRequest{Content: content}
	if clientID != "" {
		body.ClientMessageId = &clientID
	}
	buf, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/groups/"+groupID+"/messages", bytes.NewReader(buf))
	req = req.WithContext(withAuthInfo(req.Context(), &AuthInfo{UserID: userID, Role: auth.RoleUser}))
	rr := httptest.NewRecorder()
	s.SendGroupMessage(rr, req, groupID)
	return rr
}

// TestSendGroupMessageCommandStreamsPlainReply proves a group slash command is
// answered with a plain SSE reply and never enters the dispatch turn.
func TestSendGroupMessageCommandStreamsPlainReply(t *testing.T) {
	s, runner, userID, groupID := setupGroupSSE(t)
	rr := sendGroupSSE(t, s, userID, groupID, "/config now", "")

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "not available in group chats") {
		t.Fatalf("command reply missing from SSE body: %q", body)
	}
	if runner.calls != 0 {
		t.Fatalf("dispatch runner called %d times for a command, want 0", runner.calls)
	}
}

func TestSendGroupMessageFreshIngestWakesWorkerThroughPrepareSend(t *testing.T) {
	s, runner, userID, groupID := setupGroupSSE(t)
	rr := sendGroupSSE(t, s, userID, groupID, "hello team", "")

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	body := rr.Body.String()
	for _, want := range []string{`"type":"start"`, `"type":"finish"`, "data: [DONE]"} {
		if !strings.Contains(body, want) {
			t.Fatalf("SSE body missing %q: %q", want, body)
		}
	}
	if runner.calls != 1 {
		t.Fatalf("dispatcher woke %d times, want 1", runner.calls)
	}
}

// TestSendGroupMessageDedupSkipsDispatch proves a repeated client_message_id
// replays as an empty group reply and does not run a second dispatch turn.
func TestSendGroupMessageDedupSkipsDispatch(t *testing.T) {
	s, runner, userID, groupID := setupGroupSSE(t)

	if rr := sendGroupSSE(t, s, userID, groupID, "hello", "dup-1"); rr.Code != http.StatusOK {
		t.Fatalf("first send status = %d", rr.Code)
	}
	if runner.calls != 1 {
		t.Fatalf("first send dispatched %d times, want 1", runner.calls)
	}

	rr := sendGroupSSE(t, s, userID, groupID, "hello again", "dup-1")
	if rr.Code != http.StatusOK {
		t.Fatalf("replay status = %d, want 200", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "data: [DONE]") {
		t.Fatalf("replay SSE body missing [DONE]: %q", body)
	}
	if runner.calls != 1 {
		t.Fatalf("dedup replay dispatched again: %d calls, want 1", runner.calls)
	}
}

// TestSendGroupMessageForeignGroupNotFound proves a non-owner send fails opaque
// (404) before any dispatch.
func TestSendGroupMessageForeignGroupNotFound(t *testing.T) {
	s, runner, _, groupID := setupGroupSSE(t)
	rr := sendGroupSSE(t, s, "someone-else", groupID, "hello", "")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("foreign send status = %d, want 404", rr.Code)
	}
	if runner.calls != 0 {
		t.Fatalf("foreign send dispatched %d times, want 0", runner.calls)
	}
}

// TestStreamGroupEventsSnapshotsRunningTurns proves a browser that opens the
// stream mid-turn is told who is generating. Turn frames are live-only and never
// replayed, so without this snapshot every agent would read as idle until the
// next frame happened to arrive.
func TestStreamGroupEventsSnapshotsRunningTurns(t *testing.T) {
	s, _, userID, groupID, db, agentID, _ := setupGroupSSEWithDB(t)
	if rr := sendGroupSSE(t, s, userID, groupID, "hello team", ""); rr.Code != http.StatusOK {
		t.Fatalf("send status = %d", rr.Code)
	}
	q := sqlc.New(db)
	ctx := t.Context()
	messages, err := q.ListGroupMessagesAfterSeq(ctx, sqlc.ListGroupMessagesAfterSeqParams{GroupID: groupID, MinSeq: 0, BatchLimit: 1})
	if err != nil || len(messages) == 0 {
		t.Fatalf("list group messages: %v (%d rows)", err, len(messages))
	}
	members, err := q.ListGroupMembers(ctx, groupID)
	if err != nil || len(members) == 0 {
		t.Fatalf("list group members: %v (%d rows)", err, len(members))
	}
	if err := q.CreateGroupWake(ctx, sqlc.CreateGroupWakeParams{
		ID: uuid.NewString(), GroupMessageID: messages[0].ID, GroupID: groupID,
		AgentID: agentID, ReplyChannelID: members[0].ReplyChannelID,
	}); err != nil {
		t.Fatalf("create wake: %v", err)
	}
	wake, err := q.ClaimNewestGroupWake(ctx, sqlc.ClaimNewestGroupWakeParams{
		GroupID: groupID, AgentID: agentID,
		Now:        pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
		LeaseUntil: pgtype.Timestamptz{Time: time.Now().UTC().Add(time.Minute), Valid: true},
	})
	if err != nil {
		t.Fatalf("claim wake: %v", err)
	}
	if wake.Status != "running" {
		t.Fatalf("claimed wake status = %q, want running", wake.Status)
	}

	// The handler blocks until the request context ends; a short deadline lets it
	// write the replay plus the snapshot and then return.
	reqCtx, cancel := context.WithTimeout(t.Context(), 750*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/groups/"+groupID+"/events", nil)
	req = req.WithContext(withAuthInfo(reqCtx, &AuthInfo{UserID: userID, Role: auth.RoleUser}))
	rr := httptest.NewRecorder()
	s.StreamGroupEvents(rr, req, groupID, apiserver.StreamGroupEventsParams{})

	body := rr.Body.String()
	want := `event: turn` + "\n" + `data: {"agent_id":"` + agentID + `","state":"running"}`
	if !strings.Contains(body, want) {
		t.Fatalf("snapshot frame missing.\nwant: %q\nbody: %q", want, body)
	}
}

func TestAbortGroupTurnUsesAuthorizedGroupSession(t *testing.T) {
	s, runner, userID, groupID := setupGroupSSE(t)
	req := httptest.NewRequest(http.MethodPost, "/api/groups/"+groupID+"/turns/stella/abort", nil)
	req = req.WithContext(withAuthInfo(req.Context(), &AuthInfo{UserID: userID, Role: auth.RoleUser}))
	rr := httptest.NewRecorder()
	s.AbortGroupTurn(rr, req, groupID, "stella")

	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204: %s", rr.Code, rr.Body.String())
	}
	if runner.abortGroupID != groupID || runner.abortAgentID != "stella" {
		t.Fatalf("abort = (%q, %q), want (%q, %q)", runner.abortGroupID, runner.abortAgentID, groupID, "stella")
	}
}

// TestListGroupsRejectsOversizePageSize proves the handler enforces the
// documented groups page_size ceiling with 400 rather than passing an unbounded
// value into the boundary.
func TestListGroupsRejectsOversizePageSize(t *testing.T) {
	s, _, userID, _ := setupGroupSSE(t)
	over := maxGroupPageSize + 1
	req := httptest.NewRequest(http.MethodGet, "/api/groups", nil)
	req = req.WithContext(withAuthInfo(req.Context(), &AuthInfo{UserID: userID, Role: auth.RoleUser}))
	rr := httptest.NewRecorder()
	s.ListGroups(rr, req, apiserver.ListGroupsParams{PageSize: &over})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("oversize page_size status = %d, want 400 (body %q)", rr.Code, rr.Body.String())
	}
}

// TestListGroupsRejectsHugeOffsetToken proves a page token whose decoded offset
// exceeds the int32 range is rejected with 400 by the boundary's pagination
// validation, not silently truncated into a wrong page.
func TestListGroupsRejectsHugeOffsetToken(t *testing.T) {
	s, _, userID, _ := setupGroupSSE(t)
	token := encodeOffsetToken(math.MaxInt32 + 1)
	req := httptest.NewRequest(http.MethodGet, "/api/groups", nil)
	req = req.WithContext(withAuthInfo(req.Context(), &AuthInfo{UserID: userID, Role: auth.RoleUser}))
	rr := httptest.NewRecorder()
	s.ListGroups(rr, req, apiserver.ListGroupsParams{PageToken: &token})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("huge offset token status = %d, want 400 (body %q)", rr.Code, rr.Body.String())
	}
}

// TestListGroupMessagesRejectsOversizePageAndOffset proves the messages endpoint
// enforces its own (larger) page_size ceiling and rejects an out-of-range offset
// token with 400.
func TestListGroupMessagesRejectsOversizePageAndOffset(t *testing.T) {
	s, _, userID, groupID := setupGroupSSE(t)

	over := maxGroupMessagePageSize + 1
	req := httptest.NewRequest(http.MethodGet, "/api/groups/"+groupID+"/messages", nil)
	req = req.WithContext(withAuthInfo(req.Context(), &AuthInfo{UserID: userID, Role: auth.RoleUser}))
	rr := httptest.NewRecorder()
	s.ListGroupMessages(rr, req, groupID, apiserver.ListGroupMessagesParams{PageSize: &over})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("oversize messages page_size status = %d, want 400 (body %q)", rr.Code, rr.Body.String())
	}

	token := encodeOffsetToken(math.MaxInt32 + 1)
	req = httptest.NewRequest(http.MethodGet, "/api/groups/"+groupID+"/messages", nil)
	req = req.WithContext(withAuthInfo(req.Context(), &AuthInfo{UserID: userID, Role: auth.RoleUser}))
	rr = httptest.NewRecorder()
	s.ListGroupMessages(rr, req, groupID, apiserver.ListGroupMessagesParams{PageToken: &token})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("huge messages offset token status = %d, want 400 (body %q)", rr.Code, rr.Body.String())
	}
}

// sseRecorder is a minimal http.ResponseWriter+Flusher whose body stays
// readable while the handler goroutine is still writing into it. When gate is
// set the first Write blocks until it is closed — entered closes the moment
// that first Write begins, so a test can pin the handler mid-replay with the
// subscription already live. done is the request context: canceling it
// releases a blocked Write with an error so the handler can still exit.
type sseRecorder struct {
	mu        sync.Mutex
	header    http.Header
	body      bytes.Buffer
	code      int
	writeOnce sync.Once
	entered   chan struct{}
	gate      <-chan struct{}
	done      <-chan struct{}
	gateErr   bool
}

func newSSERecorder(gate <-chan struct{}, done <-chan struct{}) *sseRecorder {
	return &sseRecorder{header: http.Header{}, entered: make(chan struct{}), gate: gate, done: done}
}

func (r *sseRecorder) Header() http.Header { return r.header }

func (r *sseRecorder) WriteHeader(code int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.code == 0 {
		r.code = code
	}
}

func (r *sseRecorder) Write(p []byte) (int, error) {
	r.writeOnce.Do(func() {
		close(r.entered)
		if r.gate != nil {
			select {
			case <-r.gate:
			case <-r.done:
				r.gateErr = true
			}
		}
	})
	if r.gateErr {
		return 0, context.Canceled
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.body.Write(p)
}

func (r *sseRecorder) Flush() {}

func (r *sseRecorder) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.body.String()
}

// streamGroupEvents runs the SSE handler on a goroutine; the returned stop is
// idempotent, cancels the request context, and waits for the handler to
// return. Cleanup also calls it, so a failed test never leaks the goroutine —
// and the cancel releases a still-gated first Write instead of deadlocking.
func streamGroupEvents(t *testing.T, s *Server, userID, groupID string, since int, gate <-chan struct{}) (*sseRecorder, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	req := httptest.NewRequest(http.MethodGet, "/api/groups/"+groupID+"/events", nil)
	req = req.WithContext(withAuthInfo(ctx, &AuthInfo{UserID: userID, Role: auth.RoleUser}))
	rr := newSSERecorder(gate, ctx.Done())
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.StreamGroupEvents(rr, req, groupID, apiserver.StreamGroupEventsParams{SinceSeq: &since})
	}()
	stop := sync.OnceFunc(func() {
		cancel()
		<-done
	})
	t.Cleanup(stop)
	return rr, stop
}

// waitForSSE polls the stream body until want appears: frames land
// asynchronously on the handler goroutine.
func waitForSSE(t *testing.T, rr *sseRecorder, want string) string {
	t.Helper()
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if body := rr.String(); strings.Contains(body, want) {
			return body
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %q in SSE body: %q", want, rr.String())
	return ""
}

// claimRemoteWake commits a dispatch claim through raw sqlc — a turn executing
// on another replica, so nothing announces on this replica's hub.
func claimRemoteWake(t *testing.T, q *sqlc.Queries, groupID, agentID, triggerMessageID string) sqlc.CtxGroupDispatch {
	t.Helper()
	ctx := t.Context()
	members, err := q.ListGroupMembers(ctx, groupID)
	if err != nil || len(members) == 0 {
		t.Fatalf("list group members: %v (%d rows)", err, len(members))
	}
	if err := q.CreateGroupWake(ctx, sqlc.CreateGroupWakeParams{
		ID: uuid.NewString(), GroupMessageID: triggerMessageID, GroupID: groupID,
		AgentID: agentID, ReplyChannelID: members[0].ReplyChannelID,
	}); err != nil {
		t.Fatalf("create wake: %v", err)
	}
	wake, err := q.ClaimNewestGroupWake(ctx, sqlc.ClaimNewestGroupWakeParams{
		GroupID: groupID, AgentID: agentID,
		Now:        pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
		LeaseUntil: pgtype.Timestamptz{Time: time.Now().UTC().Add(time.Minute), Valid: true},
	})
	if err != nil {
		t.Fatalf("claim wake: %v", err)
	}
	return wake
}

// TestStreamGroupEventsHubWakeReadsDB proves a hub announce is only a wake. A
// seeded row pins the handler mid-replay — subscription live, snapshot rows
// already fetched, poll ticker not yet created — while a remote commit
// (seq=2, no local announce) and a local commit (seq=3, announced) land. The
// buffered wake must then drive a DB read that emits both rows in order; the
// announce payload is never a fact.
func TestStreamGroupEventsHubWakeReadsDB(t *testing.T) {
	s, _, userID, groupID, db, _, hub := setupGroupSSEWithDB(t)
	ctx := t.Context()
	// A second store with no hub wiring is a remote replica: its commits land
	// in the shared log without waking this replica's subscribers.
	remote := eventlog.NewStore(db)

	if _, err := remote.AppendToGroup(ctx, groupID, eventlog.GroupMessage{
		ActorType: eventlog.ActorHuman, ActorID: "remote-user", Content: "seed",
	}); err != nil {
		t.Fatalf("seed append: %v", err)
	}

	gate := make(chan struct{})
	rr, stop := streamGroupEvents(t, s, userID, groupID, 0, gate)
	// First Write in-flight means replay rows were fetched and the hub is
	// live; bound the gate so a broken setup fails instead of hanging.
	select {
	case <-rr.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("stream never reached its first write")
	}

	if _, err := remote.AppendToGroup(ctx, groupID, eventlog.GroupMessage{
		ActorType: eventlog.ActorHuman, ActorID: "remote-user", Content: "remote second",
	}); err != nil {
		t.Fatalf("remote append: %v", err)
	}
	local := eventlog.NewStore(db)
	local.OnCommitted(hub.Announce)
	if _, err := local.AppendToGroup(ctx, groupID, eventlog.GroupMessage{
		ActorType: eventlog.ActorHuman, ActorID: userID, Content: "local third",
	}); err != nil {
		t.Fatalf("local append: %v", err)
	}
	close(gate)

	body := waitForSSE(t, rr, `"seq":3`)
	if strings.Count(body, `"seq":1`) != 1 || strings.Count(body, `"seq":2`) != 1 || strings.Count(body, `"seq":3`) != 1 {
		t.Fatalf("each message must stream exactly once: %q", body)
	}
	if strings.Index(body, `"seq":2`) >= strings.Index(body, `"seq":3`) {
		t.Fatalf("seq=2 must precede seq=3: %q", body)
	}

	// A fabricated announce must not leak its payload into the stream.
	hub.Announce(eventlog.AppendResult{GroupID: groupID, Seq: 99, Message: sqlc.CtxGroupMessage{Content: "phantom"}})
	time.Sleep(300 * time.Millisecond)
	if strings.Contains(rr.String(), "phantom") {
		t.Fatalf("hub payload leaked into the stream: %q", rr.String())
	}
	stop()

	// Reconnect at the consumed cursor: replay must be gap- and dup-free.
	rr2, stop2 := streamGroupEvents(t, s, userID, groupID, 2, nil)
	body2 := waitForSSE(t, rr2, `"seq":3`)
	stop2()
	if strings.Contains(body2, `"seq":2`) || strings.Count(body2, `"seq":3`) != 1 {
		t.Fatalf("reconnect since=2 must replay only seq=3 once: %q", body2)
	}
}

// TestStreamGroupEventsPollReconcilesRemoteState proves the 1.5s poll is a
// correctness path of its own: nothing below announces on this replica's hub,
// yet a pending row's same-seq delivery flip and two consecutive identical
// terminal outcomes must still reach the stream.
func TestStreamGroupEventsPollReconcilesRemoteState(t *testing.T) {
	s, _, userID, groupID, db, agentID, _ := setupGroupSSEWithDB(t)
	ctx := t.Context()
	q := sqlc.New(db)
	remote := eventlog.NewStore(db)

	rr, stop := streamGroupEvents(t, s, userID, groupID, 0, nil)
	defer stop()

	// An accepted remote reply is born pending: the poll's message read must
	// surface it, and the later same-seq flip must re-emit the row once.
	pending, err := remote.AppendToGroup(ctx, groupID, eventlog.GroupMessage{
		ActorType: eventlog.ActorAgent, ActorID: agentID, Content: "remote reply", DeliveryState: "pending",
	})
	if err != nil {
		t.Fatalf("remote pending append: %v", err)
	}
	if body := waitForSSE(t, rr, `"delivery_state":"pending"`); strings.Count(body, `"seq":1`) != 1 {
		t.Fatalf("pending row must stream exactly once: %q", body)
	}
	if _, err := q.SetGroupMessageDeliveryState(ctx, sqlc.SetGroupMessageDeliveryStateParams{ID: pending.Message.ID, DeliveryState: "delivered"}); err != nil {
		t.Fatalf("set delivered: %v", err)
	}
	if body := waitForSSE(t, rr, `"delivery_state":"delivered"`); strings.Count(body, `"seq":1`) != 2 {
		t.Fatalf("delivered flip must re-emit seq=1 exactly once: %q", body)
	}

	// A remote turn observed mid-flight: the running frame arrives by poll
	// alone, then its terminal frame with the persisted reason.
	trigger1, err := remote.AppendToGroup(ctx, groupID, eventlog.GroupMessage{
		ActorType: eventlog.ActorHuman, ActorID: userID, Content: "poke one",
	})
	if err != nil {
		t.Fatalf("append trigger1: %v", err)
	}
	wake1 := claimRemoteWake(t, q, groupID, agentID, trigger1.Message.ID)
	waitForSSE(t, rr, `"state":"running"`)
	if _, err := q.MarkGroupDispatchSilent(ctx, sqlc.MarkGroupDispatchSilentParams{ID: wake1.ID, AttemptCount: wake1.AttemptCount, Reason: "gate one"}); err != nil {
		t.Fatalf("mark silent: %v", err)
	}
	waitForSSE(t, rr, `"reason":"gate one"`)

	// A second dispatch retires 'silent' again entirely between two reads —
	// same state text, different generation, so the frame must still land.
	trigger2, err := remote.AppendToGroup(ctx, groupID, eventlog.GroupMessage{
		ActorType: eventlog.ActorHuman, ActorID: userID, Content: "poke two",
	})
	if err != nil {
		t.Fatalf("append trigger2: %v", err)
	}
	wake2 := claimRemoteWake(t, q, groupID, agentID, trigger2.Message.ID)
	if _, err := q.MarkGroupDispatchSilent(ctx, sqlc.MarkGroupDispatchSilentParams{ID: wake2.ID, AttemptCount: wake2.AttemptCount, Reason: "gate two"}); err != nil {
		t.Fatalf("mark silent: %v", err)
	}
	body := waitForSSE(t, rr, `"reason":"gate two"`)
	if strings.Count(body, `"state":"silent"`) != 2 {
		t.Fatalf("consecutive identical terminals must both stream: %q", body)
	}
}

// A member that joins mid-stream can carry a terminal outcome the connect
// snapshot never saw: each reconcile re-reads the member list, so the frame
// still lands instead of being scoped out by the connect-frozen roster.
func TestStreamGroupEventsNewMemberTerminal(t *testing.T) {
	s, _, userID, groupID, db, _, _ := setupGroupSSEWithDB(t)
	ctx := t.Context()
	q := sqlc.New(db)

	rr, stop := streamGroupEvents(t, s, userID, groupID, 0, nil)
	defer stop()

	if _, err := q.CreateAgent(ctx, sqlc.CreateAgentParams{ID: "agent-late", Name: "Late", Workspace: t.TempDir(), Sandbox: json.RawMessage("{}"), Scope: "system", Enabled: true}); err != nil {
		t.Fatalf("create late agent: %v", err)
	}
	if err := q.CreateWebChannelIfNotExists(ctx, sqlc.CreateWebChannelIfNotExistsParams{
		ID: "ch-late", AgentID: pgtype.Text{String: "agent-late", Valid: true},
	}); err != nil {
		t.Fatalf("create late channel: %v", err)
	}
	if _, err := q.AddGroupMember(ctx, sqlc.AddGroupMemberParams{GroupID: groupID, AgentID: "agent-late", ReplyChannelID: "ch-late"}); err != nil {
		t.Fatalf("add late member: %v", err)
	}
	trigger, err := eventlog.NewStore(db).AppendToGroup(ctx, groupID, eventlog.GroupMessage{
		ActorType: eventlog.ActorHuman, ActorID: userID, Content: "hello late",
	})
	if err != nil {
		t.Fatalf("append late trigger: %v", err)
	}
	// The turn is born retired — no 'running' frame was ever observable for
	// this member on this stream.
	wake := claimRemoteWake(t, q, groupID, "agent-late", trigger.Message.ID)
	if _, err := q.MarkGroupDispatchSilent(ctx, sqlc.MarkGroupDispatchSilentParams{ID: wake.ID, AttemptCount: wake.AttemptCount, Reason: "settled elsewhere"}); err != nil {
		t.Fatalf("mark late silent: %v", err)
	}
	body := waitForSSE(t, rr, `"state":"silent"`)
	if strings.Count(body, `"agent_id":"agent-late"`) != 1 || !strings.Contains(body, `"reason":"settled elsewhere"`) {
		t.Fatalf("late member terminal must stream once with its reason: %q", body)
	}
}
