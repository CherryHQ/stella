package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"

	apitypes "github.com/CherryHQ/stella/api/types"
)

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
