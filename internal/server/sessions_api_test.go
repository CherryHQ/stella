package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	apitypes "github.com/CherryHQ/stella/api/types"
	agentsession "github.com/CherryHQ/stella/internal/agent/session"
)

func TestUpdateSessionRejectsTitleOverDomainByteBound(t *testing.T) {
	env := setupAdmin(t)
	agentID := createAgentAsUser(t, env, env.bearerToken, "Session Title Bound Agent")
	_, err := env.db.Exec(context.Background(), `
		INSERT INTO ctx_conversation (id, session_id, title, channel, kind, agent_id, user_id, last_active)
		VALUES ($1, 'session-title-bound', 'Old title', 'web', 'chat', $2, $3, now())
	`, uuid.NewString(), agentID, env.adminUser.ID)
	if err != nil {
		t.Fatalf("seed conversation: %v", err)
	}

	title := strings.Repeat("界", agentsession.MaxTitleBytes/len("界")+1)
	rr := doRequest(t, env, http.MethodPatch, "/api/agents/"+agentID+"/sessions/session-title-bound", map[string]string{"title": title})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("PATCH oversized title status = %d, want %d (body: %s)", rr.Code, http.StatusBadRequest, rr.Body.String())
	}
	var stored string
	if err := env.db.QueryRow(context.Background(), `SELECT title FROM ctx_conversation WHERE session_id = 'session-title-bound'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != "Old title" {
		t.Fatalf("stored title = %q, want unchanged", stored)
	}
}

func TestUpdateAndDeleteSession(t *testing.T) {
	env := setupAdmin(t)
	agentID := createAgentAsUser(t, env, env.bearerToken, "Session API Agent")
	_, err := env.db.Exec(context.Background(), `
		INSERT INTO ctx_conversation (id, session_id, title, channel, kind, agent_id, user_id, last_active)
		VALUES ($1, 'session-api', 'Old title', 'web', 'chat', $2, $3, now())
	`, uuid.NewString(), agentID, env.adminUser.ID)
	if err != nil {
		t.Fatalf("seed conversation: %v", err)
	}

	rr := doRequest(t, env, http.MethodPatch, "/api/agents/"+agentID+"/sessions/session-api", map[string]string{"title": "New title"})
	if rr.Code != http.StatusOK {
		t.Fatalf("PATCH session status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}
	resp := parseResponse(t, rr)
	var session apitypes.Session
	if err := json.Unmarshal(resp.Data, &session); err != nil {
		t.Fatalf("unmarshal session: %v", err)
	}
	if session.Title != "New title" {
		t.Fatalf("session title = %q, want New title", session.Title)
	}

	rr = doRequest(t, env, http.MethodDelete, "/api/agents/"+agentID+"/sessions/session-api", nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("DELETE session status = %d, want %d (body: %s)", rr.Code, http.StatusNoContent, rr.Body.String())
	}

	var archived bool
	if err := env.db.QueryRow(context.Background(), `SELECT archived FROM ctx_conversation WHERE session_id = 'session-api'`).Scan(&archived); err != nil {
		t.Fatalf("load archived flag: %v", err)
	}
	if !archived {
		t.Fatalf("archived = %v, want true", archived)
	}
}

func TestCreateMainSessionReResolvesWhenArchiveWinsPromotionRace(t *testing.T) {
	env := setupAdmin(t)
	agentID := createAgentAsUser(t, env, env.bearerToken, "Session Promotion Race Agent")
	const staleSessionID = "session-promotion-race"
	channel := agentID + ":user:" + env.adminUser.ID + ":private"
	if _, err := env.db.Exec(context.Background(), `
		INSERT INTO ctx_conversation (id, session_id, channel, kind, agent_id, user_id, last_active)
		VALUES ($1, $2, $3, 'chat', $4, $5, now())
	`, uuid.NewString(), staleSessionID, channel, agentID, env.adminUser.ID); err != nil {
		t.Fatalf("seed promotable session: %v", err)
	}

	ctx := context.Background()
	archiveTx, err := env.db.Begin(ctx)
	if err != nil {
		t.Fatalf("begin archive: %v", err)
	}
	archiveOpen := true
	defer func() {
		if archiveOpen {
			_ = archiveTx.Rollback(ctx)
		}
	}()
	// Keep the archive uncommitted so ResolveMain reads the active candidate and
	// then blocks when its promotion save reaches the row lock.
	if _, err := archiveTx.Exec(ctx, `UPDATE ctx_conversation SET archived = true WHERE session_id = $1`, staleSessionID); err != nil {
		t.Fatalf("stage archive: %v", err)
	}

	response := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		response <- doRequest(t, env, http.MethodPost, "/api/agents/"+agentID+"/sessions", map[string]string{"kind": "main"})
	}()

	deadline := time.Now().Add(5 * time.Second)
	waiting := false
	for time.Now().Before(deadline) {
		if err := env.db.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM pg_stat_activity
			WHERE datname = current_database()
			  AND wait_event_type = 'Lock'
			  AND query LIKE '%UPDATE ctx_conversation%'
		)`).Scan(&waiting); err != nil {
			t.Fatalf("observe blocked promotion: %v", err)
		}
		if waiting {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !waiting {
		_ = archiveTx.Rollback(ctx)
		archiveOpen = false
		<-response
		t.Fatal("main-session promotion never reached the archive row lock")
	}
	if err := archiveTx.Commit(ctx); err != nil {
		t.Fatalf("commit archive: %v", err)
	}
	archiveOpen = false

	rr := <-response
	if rr.Code != http.StatusCreated {
		t.Fatalf("POST main after archive race status=%d, want %d (body: %s)", rr.Code, http.StatusCreated, rr.Body.String())
	}
	resp := parseResponse(t, rr)
	var created apitypes.Session
	if err := json.Unmarshal(resp.Data, &created); err != nil {
		t.Fatalf("unmarshal created session: %v", err)
	}
	if created.Id == staleSessionID || created.Archived {
		t.Fatalf("created session = %+v, want a fresh active main", created)
	}
	var archived bool
	if err := env.db.QueryRow(ctx, `SELECT archived FROM ctx_conversation WHERE session_id = $1`, staleSessionID).Scan(&archived); err != nil {
		t.Fatalf("load raced session: %v", err)
	}
	if !archived {
		t.Fatal("the session archived by the concurrent DELETE path was revived")
	}
}

func TestSessionActivityReturnsDurableTerminalResult(t *testing.T) {
	env := setupAdmin(t)
	agentID := createAgentAsUser(t, env, env.bearerToken, "Session Activity Agent")
	_, err := env.db.Exec(context.Background(), `
		INSERT INTO ctx_conversation (
			id, session_id, title, channel, kind, agent_id, user_id, last_active,
			last_turn_started_at, last_turn_completed_at, last_turn_result
		)
		VALUES ($1, 'session-activity', 'Background turn', 'web', 'chat', $2, $3, now(), now(), now(), 'success')
	`, uuid.NewString(), agentID, env.adminUser.ID)
	if err != nil {
		t.Fatalf("seed conversation: %v", err)
	}

	listSessions := func() apitypes.Session {
		rr := doRequest(t, env, http.MethodGet, "/api/agents/"+agentID+"/sessions?kind=chat", nil)
		if rr.Code != http.StatusOK {
			t.Fatalf("GET sessions status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
		}
		resp := parseResponse(t, rr)
		var list apitypes.SessionList
		if err := json.Unmarshal(resp.Data, &list); err != nil {
			t.Fatalf("unmarshal session list: %v", err)
		}
		if len(list.Sessions) != 1 {
			t.Fatalf("sessions = %d, want 1", len(list.Sessions))
		}
		return list.Sessions[0]
	}

	got := listSessions()
	if got.ActivityStatus == nil || *got.ActivityStatus != apitypes.SessionActivityStatusSuccess {
		t.Fatalf("activity status = %v, want success", got.ActivityStatus)
	}

	rr := doRequest(t, env, http.MethodPost, "/api/agents/"+agentID+"/sessions/session-activity/view", nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("POST view status = %d, want %d (body: %s)", rr.Code, http.StatusNoContent, rr.Body.String())
	}
	viewed := listSessions()
	if viewed.ActivityStatus == nil || *viewed.ActivityStatus != apitypes.SessionActivityStatusIdle {
		t.Fatalf("viewed activity status = %v, want idle", viewed.ActivityStatus)
	}
}

func TestListSessionsHidesInternalKindsByDefault(t *testing.T) {
	env := setupAdmin(t)
	agentID := createAgentAsUser(t, env, env.bearerToken, "Session List Agent")
	rows := []struct {
		sessionID string
		kind      string
	}{
		{sessionID: "chat-session", kind: "chat"},
		{sessionID: "task-session", kind: "task"},
		{sessionID: "delegate-session", kind: "delegate"},
	}
	for _, row := range rows {
		_, err := env.db.Exec(context.Background(), `
			INSERT INTO ctx_conversation (id, session_id, title, channel, kind, agent_id, user_id, last_active)
			VALUES ($1, $2, $3, $4, $5, $6, $7, now())
		`, uuid.NewString(), row.sessionID, row.sessionID, row.kind, row.kind, agentID, env.adminUser.ID)
		if err != nil {
			t.Fatalf("seed %s conversation: %v", row.kind, err)
		}
	}

	rr := doRequest(t, env, http.MethodGet, "/api/agents/"+agentID+"/sessions", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET sessions status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}
	resp := parseResponse(t, rr)
	var list apitypes.SessionList
	if err := json.Unmarshal(resp.Data, &list); err != nil {
		t.Fatalf("unmarshal session list: %v", err)
	}
	for _, session := range list.Sessions {
		if session.Kind == "task" || session.Kind == "delegate" {
			t.Fatalf("internal session %q kind %q leaked into default list", session.Id, session.Kind)
		}
	}

	rr = doRequest(t, env, http.MethodGet, "/api/agents/"+agentID+"/sessions?kind=task", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET task sessions status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}
	resp = parseResponse(t, rr)
	list = apitypes.SessionList{}
	if err := json.Unmarshal(resp.Data, &list); err != nil {
		t.Fatalf("unmarshal task session list: %v", err)
	}
	if len(list.Sessions) != 1 || list.Sessions[0].Id != "task-session" {
		t.Fatalf("task-filtered sessions = %#v, want task-session only", list.Sessions)
	}
}
