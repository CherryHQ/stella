package server_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/internal/memory"
)

// offsetToken mirrors the server's opaque page-token encoding.
func offsetToken(offset int) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(offset)))
}

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
	if err := env.db.QueryRow(context.Background(), `SELECT id FROM ctx_conversation WHERE session_id = $1`, sessionID).Scan(&conversationID); err != nil {
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
	if len(messages.Messages) != 1 || messages.Messages[0].Role != apitypes.SessionMessageRoleAssistant {
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
	if err := env.db.QueryRow(context.Background(), `SELECT id FROM ctx_conversation WHERE session_id = $1`, sessionID).Scan(&conversationID); err != nil {
		t.Fatalf("load conversation: %v", err)
	}
	fallbackMsg1, fallbackMsg2 := uuid.NewString(), uuid.NewString()
	if _, err := env.db.Exec(context.Background(), `
		INSERT INTO ctx_message (id, conversation_id, seq, role, event_type, content, token_count, created_at, actor_type)
		VALUES
		($1, $2, 1, 'user', 'text', 'first', 10, '2026-06-10 02:00:00', $5),
		($3, $4, 2, 'assistant', 'text', 'second', 20, '2026-06-10 02:01:00', $6)
	`, fallbackMsg1, conversationID, fallbackMsg2, conversationID, eventlog.ActorHuman, eventlog.ActorAgent); err != nil {
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
	if len(first.Items) != 1 || first.Items[0].Message == nil || first.Items[0].Message.Id != fallbackMsg1 {
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
	if len(second.Items) != 1 || second.Items[0].Message == nil || second.Items[0].Message.Id != fallbackMsg2 {
		t.Fatalf("second page = %+v", second.Items)
	}
}

func TestSessionSummaryCondensedAggregatesChildren(t *testing.T) {
	env := setupAdmin(t)
	agentID := findStellaID(t, env)
	sessionID := "context-condensed-session"
	now := time.Date(2026, 6, 10, 3, 0, 0, 0, time.UTC)
	sm := env.mem.(memory.SessionManager)
	if err := sm.SaveInfo(context.Background(), memory.SessionInfo{
		ID:         sessionID,
		AgentID:    agentID,
		UserID:     env.adminUser.ID,
		Channel:    "web",
		Kind:       "chat",
		Title:      "Condensed",
		CreatedAt:  now,
		LastActive: now,
	}); err != nil {
		t.Fatalf("SaveInfo: %v", err)
	}
	var conversationID string
	if err := env.db.QueryRow(context.Background(), `SELECT id FROM ctx_conversation WHERE session_id = $1`, sessionID).Scan(&conversationID); err != nil {
		t.Fatalf("load conversation: %v", err)
	}
	mustExec := func(query string, args ...any) {
		t.Helper()
		if _, err := env.db.Exec(context.Background(), query, args...); err != nil {
			t.Fatalf("exec seed: %v\n%s", err, query)
		}
	}
	cm1, cm2, cm3, cm4 := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	mustExec(`
		INSERT INTO ctx_message (id, conversation_id, seq, role, event_type, content, token_count, created_at, actor_type)
		VALUES
		($1, $2, 1, 'user', 'text', 'one', 10, '2026-06-10 03:00:00', $9),
		($3, $4, 2, 'assistant', 'text', 'two', 10, '2026-06-10 03:01:00', $10),
		($5, $6, 3, 'user', 'text', 'three', 10, '2026-06-10 03:02:00', $11),
		($7, $8, 4, 'assistant', 'text', 'four', 10, '2026-06-10 03:03:00', $12)
	`, cm1, conversationID, cm2, conversationID, cm3, conversationID, cm4, conversationID,
		eventlog.ActorHuman, eventlog.ActorAgent, eventlog.ActorHuman, eventlog.ActorAgent)
	mustExec(`
		INSERT INTO ctx_summary (
			id, conversation_id, kind, depth, content, token_count, earliest_at, latest_at,
			descendant_count, descendant_token_count, source_message_token_count, created_at
		)
		VALUES
		('leaf-a', $1, 'epoch', 1, 'leaf a', 10, '2026-06-10 03:00:00', '2026-06-10 03:01:00', 2, 20, 20, '2026-06-10 03:04:00'),
		('leaf-b', $2, 'epoch', 1, 'leaf b', 10, '2026-06-10 03:02:00', '2026-06-10 03:03:00', 2, 20, 20, '2026-06-10 03:05:00'),
		('cond-1', $3, 'epoch', 2, 'condensed', 12, '2026-06-10 03:00:00', '2026-06-10 03:03:00', 4, 40, 40, '2026-06-10 03:06:00')
	`, conversationID, conversationID, conversationID)
	mustExec(`
		INSERT INTO ctx_summary_message (summary_id, message_id, ordinal)
		VALUES ('leaf-a', $1, 1), ('leaf-a', $2, 2), ('leaf-b', $3, 1), ('leaf-b', $4, 2)
	`, cm1, cm2, cm3, cm4)
	// Rows are (summary_id=condensed, parent_summary_id=constituent), matching
	// the write path in compaction.
	mustExec(`
		INSERT INTO ctx_summary_parent (summary_id, parent_summary_id, ordinal)
		VALUES ('cond-1', 'leaf-a', 1), ('cond-1', 'leaf-b', 2)
	`)

	rr := doRequest(t, env, http.MethodGet, "/api/agents/"+agentID+"/sessions/"+sessionID+"/summaries/cond-1", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("condensed summary: status=%d body=%s", rr.Code, rr.Body.String())
	}
	var detail apitypes.SessionSummaryDetail
	if err := json.Unmarshal(rr.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode condensed summary: %v", err)
	}
	if len(detail.Children) != 2 || detail.Children[0].Id != "leaf-a" || detail.Children[1].Id != "leaf-b" {
		t.Fatalf("children = %+v, want [leaf-a leaf-b]", detail.Children)
	}
	if detail.MessageSeqFrom == nil || *detail.MessageSeqFrom != 1 || detail.MessageSeqTo == nil || *detail.MessageSeqTo != 4 {
		t.Fatalf("seq range = %v..%v, want 1..4", detail.MessageSeqFrom, detail.MessageSeqTo)
	}
}

func TestSessionContextItemsPageTokenEdgeCases(t *testing.T) {
	env := setupAdmin(t)
	agentID := findStellaID(t, env)
	sessionID := "context-token-session"
	now := time.Date(2026, 6, 10, 4, 0, 0, 0, time.UTC)
	sm := env.mem.(memory.SessionManager)
	if err := sm.SaveInfo(context.Background(), memory.SessionInfo{
		ID:         sessionID,
		AgentID:    agentID,
		UserID:     env.adminUser.ID,
		Channel:    "web",
		Kind:       "chat",
		Title:      "Token edges",
		CreatedAt:  now,
		LastActive: now,
	}); err != nil {
		t.Fatalf("SaveInfo: %v", err)
	}
	var conversationID string
	if err := env.db.QueryRow(context.Background(), `SELECT id FROM ctx_conversation WHERE session_id = $1`, sessionID).Scan(&conversationID); err != nil {
		t.Fatalf("load conversation: %v", err)
	}
	seedContextItems(t, env, conversationID)

	rr := doRequest(t, env, http.MethodGet, "/api/agents/"+agentID+"/sessions/"+sessionID+"/context-items?page_token=not-a-token", nil)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("invalid token: status=%d body=%s", rr.Code, rr.Body.String())
	}

	// An out-of-range offset on a session that has ctx_item rows must return
	// an empty page, not fall back to raw message paging.
	rr = doRequest(t, env, http.MethodGet, "/api/agents/"+agentID+"/sessions/"+sessionID+"/context-items?page_size=10&page_token="+offsetToken(1000), nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("out-of-range token: status=%d body=%s", rr.Code, rr.Body.String())
	}
	var list apitypes.SessionContextItemList
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode out-of-range page: %v", err)
	}
	if len(list.Items) != 0 {
		t.Fatalf("out-of-range items = %+v, want empty", list.Items)
	}
	if list.NextPageToken != nil {
		t.Fatalf("out-of-range next token = %v, want nil", *list.NextPageToken)
	}
}

func TestSessionContextItemsTenantIsolation(t *testing.T) {
	env := setupAdmin(t)
	agentID := findStellaID(t, env)
	sessionID := "context-isolation-session"
	now := time.Date(2026, 6, 10, 5, 0, 0, 0, time.UTC)
	sm := env.mem.(memory.SessionManager)
	if err := sm.SaveInfo(context.Background(), memory.SessionInfo{
		ID:         sessionID,
		AgentID:    agentID,
		UserID:     env.adminUser.ID,
		Channel:    "web",
		Kind:       "chat",
		Title:      "Isolation",
		CreatedAt:  now,
		LastActive: now,
	}); err != nil {
		t.Fatalf("SaveInfo: %v", err)
	}

	// Another user's session lookup misses entirely, so the API answers 404
	// rather than 403 — existence is not leaked across tenants.
	_, otherToken := createTestUserWithToken(t, env.authStore, env.oidcStore, "context-other", auth.RoleUser)
	rr := doRequestWithSession(t, env.srv, otherToken, http.MethodGet, "/api/agents/"+agentID+"/sessions/"+sessionID+"/context-items", nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("other user context items: status=%d body=%s", rr.Code, rr.Body.String())
	}
	rr = doRequestWithSession(t, env.srv, otherToken, http.MethodGet, "/api/agents/"+agentID+"/sessions/"+sessionID+"/summaries/sum-1", nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("other user summary: status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func seedContextItems(t *testing.T, env *testEnv, conversationID string) {
	t.Helper()
	mustExec := func(query string, args ...any) {
		t.Helper()
		if _, err := env.db.Exec(context.Background(), query, args...); err != nil {
			t.Fatalf("exec seed: %v\n%s", err, query)
		}
	}
	msg1, msg2, msg3 := uuid.NewString(), uuid.NewString(), uuid.NewString()
	mustExec(`
		INSERT INTO ctx_message (id, conversation_id, seq, role, event_type, content, token_count, created_at, actor_type)
		VALUES
		($1, $2, 1, 'user', 'text', 'hello', 10, '2026-06-10 01:00:00', $7),
		($3, $4, 2, 'assistant', 'text', 'first', 20, '2026-06-10 01:01:00', $8),
		($5, $6, 3, 'assistant', 'text', 'second', 30, '2026-06-10 01:02:00', $9)
	`, msg1, conversationID, msg2, conversationID, msg3, conversationID,
		eventlog.ActorHuman, eventlog.ActorAgent, eventlog.ActorAgent)
	mustExec(`
		INSERT INTO ctx_summary (
			id, conversation_id, kind, depth, content, token_count, earliest_at, latest_at,
			descendant_count, descendant_token_count, source_message_token_count, created_at
		)
		VALUES ('sum-1', $1, 'epoch', 1, 'assistant summary', 25, '2026-06-10 01:01:00', '2026-06-10 01:02:00', 2, 50, 50, '2026-06-10 01:03:00')
	`, conversationID)
	mustExec(`
		INSERT INTO ctx_summary_message (summary_id, message_id, ordinal)
		VALUES ('sum-1', $1, 1), ('sum-1', $2, 2)
	`, msg2, msg3)
	mustExec(`
		INSERT INTO ctx_item (conversation_id, ordinal, item_type, message_id, summary_id, event_type)
		VALUES
		($1, 1, 'message', $2, NULL, 'text'),
		($3, 2, 'summary', NULL, 'sum-1', 'summary'),
		($4, 3, 'message', $5, NULL, 'text')
	`, conversationID, msg1, conversationID, conversationID, msg3)
}
