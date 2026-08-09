package access

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	delegatetool "github.com/CherryHQ/stella/internal/agent/delegate"
	agentsession "github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/lcm"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	"github.com/CherryHQ/stella/pkg/tools"
)

type staticSessionSearcher struct {
	results []memory.SearchResult
	err     error
}

func (s staticSessionSearcher) Search(context.Context, memory.Session, memory.SearchQuery) ([]memory.SearchResult, error) {
	return s.results, s.err
}

func TestSessionToolUsesRuntimeIdentityAndNewSurface(t *testing.T) {
	m := newSessionMatrix(t)
	tool := NewTool(m.svc)
	ctx := authz.WithAgentID(authz.WithUserID(context.Background(), m.owner), m.agent)

	out, err := tool.Execute(ctx, map[string]any{"action": "find", "include_archived": true})
	if err != nil {
		t.Fatal(err)
	}
	var list struct {
		Sessions []sessionCardResponse `json:"sessions"`
	}
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Sessions) != 2 {
		t.Fatalf("session count=%d, want 2", len(list.Sessions))
	}
	if strings.Contains(out, `"kind"`) {
		t.Fatalf("session kind leaked from find: %s", out)
	}
	if _, err := tool.Execute(ctx, map[string]any{"action": "get", "session_id": m.private}); err != nil {
		t.Fatalf("owner get: %v", err)
	}
	for _, removed := range []string{"list", "messages"} {
		if _, err := tool.Execute(ctx, map[string]any{"action": removed}); err == nil {
			t.Fatalf("removed action %q was accepted", removed)
		}
	}

	foreignCtx := authz.WithAgentID(authz.WithUserID(context.Background(), m.other), m.agent)
	if _, err := tool.Execute(foreignCtx, map[string]any{"action": "get", "session_id": m.private}); err == nil || !strings.Contains(err.Error(), "session not found") {
		t.Fatalf("foreign get error=%v, want hidden not found", err)
	}

	properties, ok := tool.Definition().InputSchema["properties"].(map[string]any)
	if !ok {
		t.Fatal("session tool schema has no properties")
	}
	for _, hidden := range []string{"user_id", "agent_id", "kind", "skip", "limit"} {
		if _, ok := properties[hidden]; ok {
			t.Fatalf("session tool schema must not expose %s", hidden)
		}
	}
}

func TestSessionToolCreatesAndResumesManagedSessionsSynchronously(t *testing.T) {
	m := newSessionMatrix(t)
	managedID := "legacy-delegate"
	seedManagedSession(t, m, managedID)
	runtime := &fakeRuntimeService{managedResult: delegatetool.ManagedSessionResult{SessionID: "created-delegate", Output: strings.Repeat("r", maxSessionToolResultText+1), Complete: true}}
	if err := m.svc.BindRuntimeManager(fakeRuntimeManager{svc: runtime}); err != nil {
		t.Fatal(err)
	}
	ctx := memory.WithSessionID(authz.WithAgentID(authz.WithUserID(context.Background(), m.owner), m.agent), "source-session")
	tool := NewTool(m.svc)

	out, err := tool.Execute(ctx, map[string]any{"action": "create", "message": "Review this change", "preset": "reviewer"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	var created sessionRunResponse
	if err := json.Unmarshal([]byte(out), &created); err != nil {
		t.Fatal(err)
	}
	if created.SessionID != "created-delegate" || len(created.Reply) != maxSessionToolResultText || !created.ReplyTruncated {
		t.Fatalf("create response = %#v", created)
	}
	if got := runtime.managedCalls[0]; got.SessionID != "" || got.Message != "Review this change" || got.Preset != "reviewer" {
		t.Fatalf("create request = %#v", got)
	}

	runtime.managedResult = delegatetool.ManagedSessionResult{SessionID: managedID, Output: "continued", Complete: true}
	out, err = tool.Execute(ctx, map[string]any{"action": "send", "session_id": managedID, "message": "Continue", "wait": true})
	if err != nil {
		t.Fatalf("send legacy managed session: %v", err)
	}
	var sent sessionRunResponse
	if err := json.Unmarshal([]byte(out), &sent); err != nil {
		t.Fatal(err)
	}
	if sent.SessionID != managedID || sent.Reply != "continued" || sent.ReplyTruncated {
		t.Fatalf("send response = %#v", sent)
	}
	if got := runtime.managedCalls[1]; got.SessionID != managedID || got.Preset != "" {
		t.Fatalf("send request = %#v", got)
	}

	if _, err := tool.Execute(ctx, map[string]any{"action": "create", "message": "later", "wait": false}); err == nil || !strings.Contains(err.Error(), "wait=false is not yet supported") {
		t.Fatalf("wait=false error=%v", err)
	}
	if len(runtime.managedCalls) != 2 {
		t.Fatalf("wait=false started %d managed runs, want 2 total", len(runtime.managedCalls))
	}

	getOut, err := tool.Execute(ctx, map[string]any{"action": "get", "session_id": managedID})
	if err != nil {
		t.Fatal(err)
	}
	var get sessionGetResponse
	if err := json.Unmarshal([]byte(getOut), &get); err != nil {
		t.Fatal(err)
	}
	if strings.Join(get.SupportedOperations, ",") != "get,send" {
		t.Fatalf("managed supported operations=%v", get.SupportedOperations)
	}
}

func seedManagedSession(t *testing.T, m sessionMatrix, id string) {
	t.Helper()
	now := time.Now().UTC()
	if err := m.svc.memory.SaveInfo(context.Background(), memory.SessionInfo{
		ID: id, UserID: m.owner, AgentID: m.agent,
		Kind: string(agentsession.KindDelegate), Channel: string(agentsession.ChannelDelegate), CreatedAt: now, LastActive: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlc.New(m.db).CreateConversation(context.Background(), sqlc.CreateConversationParams{
		ID: uuid.NewString(), SessionID: id,
		Kind: string(agentsession.KindDelegate), Channel: string(agentsession.ChannelDelegate), LastActive: now,
		UserID: pgtype.Text{String: m.owner, Valid: true}, AgentID: pgtype.Text{String: m.agent, Valid: true},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSessionFindSearchUsesLCMRetrievalAndExactPrincipalAgentScope(t *testing.T) {
	m := newSessionMatrix(t)
	provider, err := lcm.New(m.db.(*pgxpool.Pool), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	m.svc.searcher = provider
	ctx := authz.WithAgentID(authz.WithUserID(context.Background(), m.owner), m.agent)
	appendTranscript(t, provider, memory.Session{ID: m.private, UserID: m.owner, AgentID: m.agent},
		ai.UserMessage{Content: "rotatedtokenneedle appears in the owner session"},
		ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "owner result"}}},
	)
	// These rows prove the LCM query itself is scoped before Session policy is
	// applied. They are written through the production append path, not invented
	// role/event literals.
	appendTranscript(t, provider, memory.Session{ID: "foreign-user", UserID: m.other, AgentID: m.agent},
		ai.UserMessage{Content: "rotatedtokenneedle belongs to another user"},
	)
	appendTranscript(t, provider, memory.Session{ID: "foreign-agent", UserID: m.owner, AgentID: "other-agent"},
		ai.UserMessage{Content: "rotatedtokenneedle belongs to another agent"},
	)

	out, err := NewTool(m.svc).Execute(ctx, map[string]any{"action": "find", "query": "rotatedtokenneedle"})
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Sessions []sessionCardResponse `json:"sessions"`
	}
	if err := json.Unmarshal([]byte(out), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Sessions) != 1 || response.Sessions[0].ID != m.private {
		t.Fatalf("scoped search sessions=%#v, want only %s", response.Sessions, m.private)
	}
	card := response.Sessions[0]
	if !strings.Contains(card.Match, "rotatedtokenneedle") || card.MatchCursor == "" {
		t.Fatalf("search card lacks bounded match/cursor: %#v", card)
	}
	for _, leaked := range []string{"source_type", "source_id", `"kind"`, "summary_id", "depth"} {
		if strings.Contains(out, leaked) {
			t.Fatalf("search leaked LCM detail %q: %s", leaked, out)
		}
	}

	getOut, err := NewTool(m.svc).Execute(ctx, map[string]any{
		"action": "get", "session_id": m.private, "cursor": card.MatchCursor, "transcript_page_size": 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(getOut, "rotatedtokenneedle") {
		t.Fatalf("get around find match missed matching turn: %s", getOut)
	}

	foreignCtx := authz.WithAgentID(authz.WithUserID(context.Background(), m.other), m.agent)
	foreignOut, err := NewTool(m.svc).Execute(foreignCtx, map[string]any{"action": "find", "query": "rotatedtokenneedle"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(foreignOut, m.private) {
		t.Fatalf("cross-user search exposed owner session: %s", foreignOut)
	}
}

func TestSessionFindKeepsCardWhenSearchSourceWasDeleted(t *testing.T) {
	m := newSessionMatrix(t)
	provider, err := lcm.New(m.db.(*pgxpool.Pool), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := authz.WithAgentID(authz.WithUserID(context.Background(), m.owner), m.agent)
	appendTranscript(t, provider, memory.Session{ID: m.private, UserID: m.owner, AgentID: m.agent},
		ai.UserMessage{Content: "stale anchor still leaves a useful search card"},
	)
	var sourceID string
	if err := m.db.QueryRow(ctx, `
		SELECT m.id
		FROM ctx_message m
		JOIN ctx_conversation c ON c.id = m.conversation_id
		WHERE c.session_id = $1
		ORDER BY m.seq DESC
		LIMIT 1`, m.private).Scan(&sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := m.db.Exec(ctx, "DELETE FROM ctx_item WHERE message_id = $1", sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := m.db.Exec(ctx, "DELETE FROM ctx_message WHERE id = $1", sourceID); err != nil {
		t.Fatal(err)
	}
	m.svc.searcher = staticSessionSearcher{results: []memory.SearchResult{{
		SourceType: "message", SourceID: sourceID, SessionID: m.private,
		Content: "stale anchor still leaves a useful search card",
	}}}

	out, err := NewTool(m.svc).Execute(ctx, map[string]any{"action": "find", "query": "stale anchor"})
	if err != nil {
		t.Fatalf("find should degrade a deleted match anchor: %v", err)
	}
	var response struct {
		Sessions []sessionCardResponse `json:"sessions"`
	}
	if err := json.Unmarshal([]byte(out), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Sessions) != 1 || response.Sessions[0].ID != m.private {
		t.Fatalf("find dropped card after source deletion: %#v", response.Sessions)
	}
	if response.Sessions[0].Match == "" || response.Sessions[0].MatchCursor != "" {
		t.Fatalf("deleted source should retain match and omit cursor: %#v", response.Sessions[0])
	}
}

func TestSessionGetIsCompactByDefaultAndPagesWholeToolTurns(t *testing.T) {
	m := newSessionMatrix(t)
	provider, err := lcm.New(m.db.(*pgxpool.Pool), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := authz.WithAgentID(authz.WithUserID(context.Background(), m.owner), m.agent)
	appendTranscript(t, provider, memory.Session{ID: m.private, UserID: m.owner, AgentID: m.agent},
		ai.UserMessage{Content: "first request"},
		ai.AssistantMessage{Content: []ai.ContentBlock{ai.ToolCall{ID: "call-1", Name: "test_tool", Arguments: map[string]any{"path": "/tmp"}}}},
		ai.ToolResultMessage{ToolCallID: "call-1", ToolName: "test_tool", Content: []ai.ContentBlock{ai.TextContent{Text: strings.Repeat("x", maxSessionToolOutputText+100)}}},
		ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "first answer"}}},
		ai.UserMessage{Content: "second request"},
		ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "second answer"}}},
	)

	tool := NewTool(m.svc)
	compactOut, err := tool.Execute(ctx, map[string]any{"action": "get", "session_id": m.private})
	if err != nil {
		t.Fatal(err)
	}
	var compact sessionGetResponse
	if err := json.Unmarshal([]byte(compactOut), &compact); err != nil {
		t.Fatal(err)
	}
	if compact.Transcript != nil || compact.TranscriptCursor == "" || len(compact.Preview) != 1 {
		t.Fatalf("get default was not compact/cursor-gated: %#v", compact)
	}
	if compact.ActiveRequest != nil || strings.Join(compact.SupportedOperations, ",") != "get,send" {
		t.Fatalf("unexpected compact state: %#v", compact)
	}

	pageOut, err := tool.Execute(ctx, map[string]any{
		"action": "get", "session_id": m.private, "cursor": compact.TranscriptCursor, "transcript_page_size": 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	var page sessionGetResponse
	if err := json.Unmarshal([]byte(pageOut), &page); err != nil {
		t.Fatal(err)
	}
	if page.Transcript == nil || len(page.Transcript.Turns) != 1 || !page.Transcript.HasMore || page.Transcript.NextCursor == "" {
		t.Fatalf("unexpected latest page: %#v", page.Transcript)
	}
	if got := page.Transcript.Turns[0].Messages[0].Content; got != "second request" {
		t.Fatalf("latest turn begins %q, want second request", got)
	}

	olderOut, err := tool.Execute(ctx, map[string]any{
		"action": "get", "session_id": m.private, "cursor": page.Transcript.NextCursor, "transcript_page_size": 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(olderOut), &page); err != nil {
		t.Fatal(err)
	}
	messages := page.Transcript.Turns[0].Messages
	if len(messages) != 4 {
		t.Fatalf("tool turn split across page: %#v", messages)
	}
	if messages[1].Type != "tool_call" || messages[2].Type != "tool_result" || messages[1].ToolCallID != messages[2].ToolCallID {
		t.Fatalf("tool call/result pairing lost: %#v", messages)
	}
	if !messages[2].Truncated || len(messages[2].Content) != maxSessionToolOutputText {
		t.Fatalf("oversized tool output not bounded: len=%d truncated=%v", len(messages[2].Content), messages[2].Truncated)
	}
}

func TestSessionToolTranscriptProjectionHidesMultimodalBaselines(t *testing.T) {
	remaining := maxSessionToolResultText
	message := sessionToolMessageFrom(Message{
		ID: "multimodal", Role: "user", EventType: "multimodal", Content: "private model baseline",
		Parts: []MessagePart{{Type: "text", Text: "visible text"}, {Type: "image", MediaID: "media", MimeType: "image/png"}},
	}, &remaining)
	encoded, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "private model baseline") || !strings.Contains(string(encoded), "visible text") {
		t.Fatalf("unsafe transcript projection: %s", encoded)
	}
}

func TestSessionToolRejectsInvalidCursorsAndOffsets(t *testing.T) {
	m := newSessionMatrix(t)
	ctx := authz.WithAgentID(authz.WithUserID(context.Background(), m.owner), m.agent)
	tool := NewTool(m.svc)

	if _, err := tool.Execute(ctx, map[string]any{
		"action": "find", "page_token": tools.OffsetToken(math.MaxInt32),
	}); err == nil || !strings.Contains(err.Error(), "invalid pagination") {
		t.Fatalf("large find offset error=%v, want invalid pagination", err)
	}
	if _, err := tool.Execute(ctx, map[string]any{
		"action": "get", "session_id": m.private, "cursor": "not-a-cursor",
	}); err == nil || !strings.Contains(err.Error(), "invalid transcript cursor") {
		t.Fatalf("invalid get cursor error=%v", err)
	}
	otherCursor, err := encodeTranscriptCursor(transcriptCursor{Version: sessionTranscriptCursorVersion, SessionID: m.internal})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Execute(ctx, map[string]any{
		"action": "get", "session_id": m.private, "cursor": otherCursor,
	}); err == nil || !strings.Contains(err.Error(), "invalid transcript cursor") {
		t.Fatalf("cross-session cursor error=%v", err)
	}
}

func appendTranscript(t *testing.T, provider *lcm.Provider, session memory.Session, messages ...ai.Message) {
	t.Helper()
	if err := provider.Append(context.Background(), session, messages...); err != nil {
		t.Fatalf("append production transcript for %s: %v", session.ID, err)
	}
}
