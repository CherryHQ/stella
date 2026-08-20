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
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	apiserver "github.com/CherryHQ/stella/api/server"
	apitypes "github.com/CherryHQ/stella/api/types"
	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/channel"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/eventlog"
	cfgstore "github.com/CherryHQ/stella/internal/store"
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
	s, runner, userID, groupID, _, _ = setupGroupSSEWithDB(t)
	return s, runner, userID, groupID
}

// setupGroupSSEWithDB is setupGroupSSE plus the handles a test needs to write
// dispatch rows directly: the pool and the member agent id.
func setupGroupSSEWithDB(t *testing.T) (s *Server, runner *fakeGroupRunner, userID, groupID string, db *pgxpool.Pool, agentID string) {
	t.Helper()
	db = dbtest.New(t)
	store := cfgstore.NewDBStore(db)
	ctx := context.Background()
	if err := store.Seed(ctx); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	as := appdb.NewAuthStore(db)
	oidc := appdb.NewOIDCStore(db)
	agentAccess := agentaccess.NewService(store, as)
	runner = &fakeGroupRunner{}
	groupSvc := channel.NewGroupService(db, agentAccess, channel.NewRuntimeResolver(store), eventlog.NewStore(db), runner, channel.WithGroupEventHub(channel.NewGroupEventHub()))

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
		runtimeCtx: context.Background(),
	}
	return s, runner, user.ID, g.ID, db, stella
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

func TestSendGroupMessageFreshIngestWakesWorker(t *testing.T) {
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
	s, _, userID, groupID, db, agentID := setupGroupSSEWithDB(t)
	if rr := sendGroupSSE(t, s, userID, groupID, "hello team", ""); rr.Code != http.StatusOK {
		t.Fatalf("send status = %d", rr.Code)
	}
	q := sqlc.New(db)
	ctx := context.Background()
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
	reqCtx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
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
