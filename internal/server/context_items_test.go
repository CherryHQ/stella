package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/memory"
)

func TestSessionContextItemsAndSummaryEndpoints(t *testing.T) {
	env := setupAdmin(t)
	agentID := findStellaID(t, env)
	sessionID := "context-items-session"
	now := time.Date(2026, 6, 10, 1, 0, 0, 0, time.UTC)
	sm := env.mem.(memory.SessionManager)
	if err := sm.SaveInfo(context.Background(), memory.SessionInfo{
		ID:         sessionID,
		AgentID:    agentID,
		UserID:     env.adminUser.ID,
		Channel:    "web",
		Kind:       "chat",
		Title:      "Context items",
		CreatedAt:  now,
		LastActive: now,
	}); err != nil {
		t.Fatalf("SaveInfo: %v", err)
	}

	var conversationID string
	if err := env.db.QueryRow(`SELECT id FROM ctx_conversation WHERE session_id = ?`, sessionID).Scan(&conversationID); err != nil {
		t.Fatalf("load conversation: %v", err)
	}
	seedContextItems(t, env, conversationID)

	rr := doRequest(t, env, http.MethodGet, "/api/agents/"+agentID+"/sessions/"+sessionID+"/context-items?page_size=10", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("context items: status=%d body=%s", rr.Code, rr.Body.String())
	}
	var list apitypes.SessionContextItemList
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode context items: %v", err)
	}
	if len(list.Items) != 3 {
		t.Fatalf("items len = %d, want 3: %+v", len(list.Items), list.Items)
	}
	if list.Items[0].Type != apitypes.Message || list.Items[1].Type != apitypes.Summary {
		t.Fatalf("item ordering/types = %+v", list.Items)
	}
	if list.Meta.MessageCount != 3 || list.Meta.SourceTokenCount != 60 || list.Meta.ActiveTokenCount != 65 || list.Meta.SummaryDepth != 1 {
		t.Fatalf("meta = %+v", list.Meta)
	}

	rr = doRequest(t, env, http.MethodGet, "/api/agents/"+agentID+"/sessions/"+sessionID+"/summaries/sum-1", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("summary: status=%d body=%s", rr.Code, rr.Body.String())
	}
	var detail apitypes.SessionSummaryDetail
	if err := json.Unmarshal(rr.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode summary: %v", err)
	}
	if detail.MessageSeqFrom == nil || *detail.MessageSeqFrom != 2 || detail.MessageSeqTo == nil || *detail.MessageSeqTo != 3 {
		t.Fatalf("seq range = %v..%v, want 2..3", detail.MessageSeqFrom, detail.MessageSeqTo)
	}

	rr = doRequest(t, env, http.MethodGet, "/api/agents/"+agentID+"/sessions/"+sessionID+"/messages?seq_from=2&seq_to=3", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("messages seq range: status=%d body=%s", rr.Code, rr.Body.String())
	}
	var messages apitypes.SessionMessageList
	if err := json.Unmarshal(rr.Body.Bytes(), &messages); err != nil {
		t.Fatalf("decode messages: %v", err)
	}
	if len(messages.Messages) != 1 || messages.Messages[0]["role"] != "assistant" {
		t.Fatalf("messages = %+v, want merged assistant range", messages.Messages)
	}
}

func TestSessionContextItemsFallbackUsesMessagePaging(t *testing.T) {
	env := setupAdmin(t)
	agentID := findStellaID(t, env)
	sessionID := "context-items-fallback-session"
	now := time.Date(2026, 6, 10, 2, 0, 0, 0, time.UTC)
	sm := env.mem.(memory.SessionManager)
	if err := sm.SaveInfo(context.Background(), memory.SessionInfo{
		ID:         sessionID,
		AgentID:    agentID,
		UserID:     env.adminUser.ID,
		Channel:    "web",
		Kind:       "chat",
		Title:      "Context fallback",
		CreatedAt:  now,
		LastActive: now,
	}); err != nil {
		t.Fatalf("SaveInfo: %v", err)
	}

	var conversationID string
	if err := env.db.QueryRow(`SELECT id FROM ctx_conversation WHERE session_id = ?`, sessionID).Scan(&conversationID); err != nil {
		t.Fatalf("load conversation: %v", err)
	}
	if _, err := env.db.Exec(`
		INSERT INTO ctx_message (id, conversation_id, seq, role, event_type, content, token_count, created_at)
		VALUES
		('fallback-msg-1', ?, 1, 'user', 'text', 'first', 10, '2026-06-10 02:00:00'),
		('fallback-msg-2', ?, 2, 'assistant', 'text', 'second', 20, '2026-06-10 02:01:00')
	`, conversationID, conversationID); err != nil {
		t.Fatalf("seed fallback messages: %v", err)
	}

	rr := doRequest(t, env, http.MethodGet, "/api/agents/"+agentID+"/sessions/"+sessionID+"/context-items?page_size=1", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("context fallback: status=%d body=%s", rr.Code, rr.Body.String())
	}
	var first apitypes.SessionContextItemList
	if err := json.Unmarshal(rr.Body.Bytes(), &first); err != nil {
		t.Fatalf("decode first page: %v", err)
	}
	if len(first.Items) != 1 || first.Items[0].Message == nil || first.Items[0].Message.Id != "fallback-msg-1" {
		t.Fatalf("first page = %+v", first.Items)
	}
	if first.Meta.SourceTokenCount != 30 || first.Meta.ActiveTokenCount != 30 {
		t.Fatalf("fallback meta = %+v", first.Meta)
	}
	if first.NextPageToken == nil {
		t.Fatal("missing next page token")
	}

	rr = doRequest(t, env, http.MethodGet, "/api/agents/"+agentID+"/sessions/"+sessionID+"/context-items?page_size=1&page_token="+*first.NextPageToken, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("context fallback second page: status=%d body=%s", rr.Code, rr.Body.String())
	}
	var second apitypes.SessionContextItemList
	if err := json.Unmarshal(rr.Body.Bytes(), &second); err != nil {
		t.Fatalf("decode second page: %v", err)
	}
	if len(second.Items) != 1 || second.Items[0].Message == nil || second.Items[0].Message.Id != "fallback-msg-2" {
		t.Fatalf("second page = %+v", second.Items)
	}
}

func seedContextItems(t *testing.T, env *testEnv, conversationID string) {
	t.Helper()
	mustExec := func(query string, args ...any) {
		t.Helper()
		if _, err := env.db.Exec(query, args...); err != nil {
			t.Fatalf("exec seed: %v\n%s", err, query)
		}
	}
	mustExec(`
		INSERT INTO ctx_message (id, conversation_id, seq, role, event_type, content, token_count, created_at)
		VALUES
		('msg-1', ?, 1, 'user', 'text', 'hello', 10, '2026-06-10 01:00:00'),
		('msg-2', ?, 2, 'assistant', 'text', 'first', 20, '2026-06-10 01:01:00'),
		('msg-3', ?, 3, 'assistant', 'text', 'second', 30, '2026-06-10 01:02:00')
	`, conversationID, conversationID, conversationID)
	mustExec(`
		INSERT INTO ctx_summary (
			id, conversation_id, kind, depth, content, token_count, earliest_at, latest_at,
			descendant_count, descendant_token_count, source_message_token_count, created_at
		)
		VALUES ('sum-1', ?, 'epoch', 1, 'assistant summary', 25, '2026-06-10 01:01:00', '2026-06-10 01:02:00', 2, 50, 50, '2026-06-10 01:03:00')
	`, conversationID)
	mustExec(`
		INSERT INTO ctx_summary_message (summary_id, message_id, ordinal)
		VALUES ('sum-1', 'msg-2', 1), ('sum-1', 'msg-3', 2)
	`)
	mustExec(`
		INSERT INTO ctx_item (conversation_id, ordinal, item_type, message_id, summary_id, event_type)
		VALUES
		(?, 1, 'message', 'msg-1', NULL, 'text'),
		(?, 2, 'summary', NULL, 'sum-1', 'summary'),
		(?, 3, 'message', 'msg-3', NULL, 'text')
	`, conversationID, conversationID, conversationID)
}
