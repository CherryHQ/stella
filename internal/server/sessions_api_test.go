package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

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
