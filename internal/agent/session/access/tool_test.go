package access

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	"github.com/CherryHQ/stella/pkg/tools"
)

func TestSessionToolUsesRuntimeIdentity(t *testing.T) {
	m := newSessionMatrix(t)
	tool := NewTool(m.svc)
	ctx := authz.WithAgentID(authz.WithUserID(context.Background(), m.owner), m.agent)

	out, err := tool.Execute(ctx, map[string]any{"action": "list", "include_archived": true})
	if err != nil {
		t.Fatal(err)
	}
	var list struct {
		Sessions []sessionToolResponse `json:"sessions"`
	}
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Sessions) != 2 {
		t.Fatalf("session count=%d, want 2", len(list.Sessions))
	}
	if _, err := tool.Execute(ctx, map[string]any{"action": "get", "session_id": m.private}); err != nil {
		t.Fatalf("owner get: %v", err)
	}

	foreignCtx := authz.WithAgentID(authz.WithUserID(context.Background(), m.other), m.agent)
	if _, err := tool.Execute(foreignCtx, map[string]any{"action": "get", "session_id": m.private}); err == nil || !strings.Contains(err.Error(), "session not found") {
		t.Fatalf("foreign get error=%v, want hidden not found", err)
	}

	properties, ok := tool.Definition().InputSchema["properties"].(map[string]any)
	if !ok {
		t.Fatal("session tool schema has no properties")
	}
	if _, ok := properties["user_id"]; ok {
		t.Fatal("session tool schema must not expose user identity")
	}
}

func TestSessionToolMessagesAreBoundedAndPaged(t *testing.T) {
	m := newSessionMatrix(t)
	ctx := authz.WithAgentID(authz.WithUserID(context.Background(), m.owner), m.agent)
	conv, err := m.svc.q.GetConversationBySessionID(ctx, sqlc.GetConversationBySessionIDParams{
		SessionID: m.private,
		UserID:    pgtype.Text{String: m.owner, Valid: true},
		AgentID:   pgtype.Text{String: m.agent, Valid: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	for seq, content := range []string{strings.Repeat("x", maxSessionToolMessageText+1), "second"} {
		if _, err := m.svc.q.CreateMessage(ctx, sqlc.CreateMessageParams{
			ID: uuid.NewString(), ConversationID: conv.ID, Seq: int64(seq + 1), Role: "user",
			EventType: "message", Content: content,
		}); err != nil {
			t.Fatal(err)
		}
	}

	out, err := NewTool(m.svc).Execute(ctx, map[string]any{
		"action": "messages", "session_id": m.private, "limit": 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	var page struct {
		Messages []sessionToolMessage `json:"messages"`
		HasMore  bool                 `json:"has_more"`
		NextSkip int                  `json:"next_skip"`
	}
	if err := json.Unmarshal([]byte(out), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 1 || page.Messages[0].Content != "second" || !page.HasMore || page.NextSkip != 1 {
		t.Fatalf("unexpected page: %#v", page)
	}

	out, err = NewTool(m.svc).Execute(ctx, map[string]any{
		"action": "messages", "session_id": m.private, "limit": 1, "skip": 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(out), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 1 || page.HasMore || !page.Messages[0].Truncated || len(page.Messages[0].Content) != maxSessionToolMessageText {
		t.Fatalf("older message was not bounded: %#v", page)
	}
}

func TestSessionToolPreservesLogicalAssistantTurnsAndHidesBaselines(t *testing.T) {
	groups := groupLogicalMessages([]Message{
		{ID: "user", Role: "user"},
		{ID: "assistant-text", Role: "assistant"},
		{ID: "assistant-tool", Role: "assistant"},
	})
	if len(groups) != 2 || len(groups[1]) != 2 {
		t.Fatalf("logical groups=%#v", groups)
	}

	remaining := maxSessionToolResultText
	message := sessionToolMessageFrom(Message{
		ID: "multimodal", Role: "user", Content: "private model baseline",
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

func TestSessionToolMessageBudgetPreservesNewestContent(t *testing.T) {
	messages := make([]Message, 0, 7)
	for i := range 6 {
		messages = append(messages, Message{ID: fmt.Sprintf("old-%d", i), Content: strings.Repeat("x", maxSessionToolMessageText)})
	}
	messages = append(messages, Message{ID: "newest", Content: "latest result"})

	items := sessionToolMessagesFrom(messages)
	if got := items[len(items)-1].Content; got != "latest result" {
		t.Fatalf("newest content=%q, want latest result", got)
	}
	if !items[0].Truncated {
		t.Fatal("oldest content should yield budget to newer messages")
	}
}

func TestSessionToolRejectsOffsetsThatCannotFitDatabasePagination(t *testing.T) {
	m := newSessionMatrix(t)
	ctx := authz.WithAgentID(authz.WithUserID(context.Background(), m.owner), m.agent)
	tool := NewTool(m.svc)

	if _, err := tool.Execute(ctx, map[string]any{
		"action": "list", "page_token": tools.OffsetToken(math.MaxInt32),
	}); err == nil || !strings.Contains(err.Error(), "invalid pagination") {
		t.Fatalf("large list offset error=%v, want invalid pagination", err)
	}
	if _, err := tool.Execute(ctx, map[string]any{
		"action": "messages", "session_id": m.private, "skip": math.MaxInt32,
	}); err == nil || !strings.Contains(err.Error(), "invalid message pagination") {
		t.Fatalf("large message offset error=%v, want invalid message pagination", err)
	}
}
