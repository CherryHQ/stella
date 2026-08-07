package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/agent"
	agentsession "github.com/CherryHQ/stella/internal/agent/session"
	sessionaccess "github.com/CherryHQ/stella/internal/agent/session/access"
)

// recordingRuntime stands in for the agent pool and reports whether a turn was
// ever started. Nothing else in this test can tell the difference between "the
// send was rejected" and "the send ran and then failed".
type recordingRuntime struct {
	chats atomic.Int64
	stops atomic.Int64
}

func (r *recordingRuntime) Chat(context.Context, agent.ChatRequest) <-chan agent.Event {
	r.chats.Add(1)
	ch := make(chan agent.Event)
	close(ch)
	return ch
}

func (r *recordingRuntime) StopSession(context.Context, string) bool {
	r.stops.Add(1)
	return true
}

func (r *recordingRuntime) SubscribeSession(string) (<-chan agent.Event, func()) {
	ch := make(chan agent.Event)
	close(ch)
	return ch, func() {}
}

func (r *recordingRuntime) SessionLive(string) bool { return false }

func (r *recordingRuntime) CompactAuthorizedSession(context.Context, agentsession.Info) (string, error) {
	return "", nil
}

type recordingRuntimeManager struct{ rt *recordingRuntime }

func (m recordingRuntimeManager) GetService(string) sessionaccess.RuntimeService { return m.rt }
func (m recordingRuntimeManager) Default() sessionaccess.RuntimeService          { return m.rt }

// TestSendToArchivedSessionConflicts covers the archived-send answer at the
// transport, which is where the distinction actually matters. `/new` archives
// a session out from under whatever is already
// holding it — a browser tab, a mobile client — and that client needs to learn
// it must move to the successor. 404 would read as a broken link and 500 as a
// server fault; only 409 says "your session is gone, get the new one".
func TestSendToArchivedSessionConflicts(t *testing.T) {
	env := setupAdmin(t)
	rt := &recordingRuntime{}
	if err := env.deps.SessionAccess.BindRuntimeManager(recordingRuntimeManager{rt: rt}); err != nil {
		t.Fatalf("BindRuntimeManager: %v", err)
	}
	agentID := createAgentAsUser(t, env, env.bearerToken, "Archived Send Agent")
	ctx := context.Background()

	if _, err := env.db.Exec(ctx, `
		INSERT INTO ctx_conversation (id, session_id, title, channel, kind, agent_id, user_id, archived, last_active)
		VALUES ($1, 'archived-send', 'Rotated away', 'web', 'chat', $2, $3, true, now())
	`, uuid.NewString(), agentID, env.adminUser.ID); err != nil {
		t.Fatalf("seed archived conversation: %v", err)
	}

	rr := doRequest(t, env, http.MethodPost,
		"/api/agents/"+agentID+"/sessions/archived-send/messages",
		map[string]any{"parts": []map[string]string{{"type": "text", "text": "still there?"}}})
	if rr.Code != http.StatusConflict {
		t.Fatalf("POST to archived session = %d, want %d (body: %s)", rr.Code, http.StatusConflict, rr.Body.String())
	}

	// The body is the error envelope, not a half-open SSE stream: a client that
	// already switched to reading events would otherwise hang on a dead turn.
	var body struct {
		Error struct {
			Code    int    `json:"code"`
			Status  string `json:"status"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal error body %q: %v", rr.Body.String(), err)
	}
	if body.Error.Code != http.StatusConflict || body.Error.Status != "ABORTED" {
		t.Fatalf("error envelope = %+v, want a 409/ABORTED", body.Error)
	}
	if body.Error.Message == "" {
		t.Fatal("409 carried no message; the client has nothing to show or act on")
	}
	if n := rt.chats.Load(); n != 0 {
		t.Fatalf("the runtime started %d turns on an archived session, want 0", n)
	}

	// A live session on the same agent still sends, so the 409 is about this
	// session's state and not a blanket rejection.
	if _, err := env.db.Exec(ctx, `
		INSERT INTO ctx_conversation (id, session_id, channel, kind, agent_id, user_id, last_active)
		VALUES ($1, 'live-send', 'web', 'chat', $2, $3, now())
	`, uuid.NewString(), agentID, env.adminUser.ID); err != nil {
		t.Fatalf("seed live conversation: %v", err)
	}
	rr = doRequest(t, env, http.MethodPost,
		"/api/agents/"+agentID+"/sessions/live-send/messages",
		map[string]any{"parts": []map[string]string{{"type": "text", "text": "still there?"}}})
	if rr.Code != http.StatusOK {
		t.Fatalf("POST to live session = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}
	if n := rt.chats.Load(); n != 1 {
		t.Fatalf("runtime turns after a live send = %d, want 1", n)
	}

	rr = doRequest(t, env, http.MethodPost,
		"/api/agents/"+agentID+"/sessions/live-send/stop", nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("POST stop live session = %d, want %d (body: %s)", rr.Code, http.StatusNoContent, rr.Body.String())
	}
	if n := rt.stops.Load(); n != 1 {
		t.Fatalf("runtime stops = %d, want 1", n)
	}
}
