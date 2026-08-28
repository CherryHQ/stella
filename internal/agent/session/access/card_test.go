package access

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	agentsession "github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

type sessionSummaryCountingDB struct {
	db      sqlc.DBTX
	queries int
}

func TestSessionSendableMatchesConversationSendPolicy(t *testing.T) {
	for _, tc := range []struct {
		kind     agentsession.Kind
		archived bool
		want     bool
	}{
		{kind: agentsession.KindMain, want: true},
		{kind: agentsession.KindChat, want: true},
		{kind: agentsession.KindDelegate, want: true},
		{kind: agentsession.KindTask},
		{kind: agentsession.KindScheduler},
		{kind: agentsession.KindChat, archived: true},
	} {
		if got := sessionSendable(agentsession.Info{Kind: string(tc.kind), Archived: tc.archived}); got != tc.want {
			t.Fatalf("sessionSendable(kind=%s archived=%v)=%v, want %v", tc.kind, tc.archived, got, tc.want)
		}
	}
}

func (d *sessionSummaryCountingDB) Exec(ctx context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	return d.db.Exec(ctx, query, args...)
}

func (d *sessionSummaryCountingDB) Query(ctx context.Context, query string, args ...any) (pgx.Rows, error) {
	if strings.Contains(query, "ListConversationSummarySourceBySessionIDs") {
		d.queries++
	}
	return d.db.Query(ctx, query, args...)
}

func (d *sessionSummaryCountingDB) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	return d.db.QueryRow(ctx, query, args...)
}

func TestSessionCardsDeriveSummaryStateAndSendabilityInOneBatch(t *testing.T) {
	m := newSessionMatrix(t)
	ctx := authz.WithAgentID(authz.WithUserID(context.Background(), m.owner), m.agent)
	q := sqlc.New(m.db)
	conversation, err := q.GetConversationBySessionID(ctx, sqlc.GetConversationBySessionIDParams{
		SessionID: m.private,
		UserID:    pgtype.Text{String: m.owner, Valid: true},
		AgentID:   pgtype.Text{String: m.agent, Valid: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	messages := []struct {
		role, eventType, content string
	}{
		{"user", "text", "Initial authentication question"},
		{"assistant", "text", "Old answer that must not define the recent tail"},
		{"user", "text", "Investigate refresh-token concurrency"},
		{"assistant", "thinking", "private chain of thought"},
		{"assistant", "tool_call", "dangerous intermediate tool payload"},
		{"assistant", "text", "Use a single-flight lock and validate cross-node behavior"},
		{"user", "text", "continue"},
		{"user", "multimodal", `[{"kind":"image","data":"RAW-BASE64-IMAGE-DATA"}]`},
	}
	for i, message := range messages {
		actorType := eventlog.ActorHuman
		if message.role != "user" {
			actorType = eventlog.ActorAgent
		}
		if _, err := q.CreateMessage(ctx, sqlc.CreateMessageParams{
			ID: uuid.NewString(), ConversationID: conversation.ID, Seq: int64(i + 1),
			Role: message.role, EventType: message.eventType, Content: message.content, TokenCount: 1, ActorType: string(actorType),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := q.CreateSummary(ctx, sqlc.CreateSummaryParams{
		ID: "summary-latest", ConversationID: conversation.ID, Kind: "leaf", Depth: 0,
		Content: "Earlier work established rotating refresh tokens", TokenCount: 8,
	}); err != nil {
		t.Fatal(err)
	}
	info, err := m.svc.memory.LoadInfo(ctx, m.private)
	if err != nil {
		t.Fatal(err)
	}
	info.Title = "Authentication refactor"
	info.LastTurnResult = memory.SessionTurnSuccess
	info.LastTurnStartedAt = time.Date(2026, 8, 9, 9, 58, 12, 0, time.FixedZone("offset", 8*60*60))
	if err := m.svc.memory.SaveInfo(ctx, info); err != nil {
		t.Fatal(err)
	}
	controlConversation, err := q.GetConversationBySessionID(ctx, sqlc.GetConversationBySessionIDParams{
		SessionID: m.internal,
		UserID:    pgtype.Text{String: m.owner, Valid: true},
		AgentID:   pgtype.Text{String: m.agent, Valid: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.CreateMessage(ctx, sqlc.CreateMessageParams{
		ID: uuid.NewString(), ConversationID: controlConversation.ID, Seq: 1,
		Role: "assistant", EventType: "tool_call", Content: "internal payload", TokenCount: 1, ActorType: string(eventlog.ActorAgent),
	}); err != nil {
		t.Fatal(err)
	}
	controlInfo, err := m.svc.memory.LoadInfo(ctx, m.internal)
	if err != nil {
		t.Fatal(err)
	}
	if applied, err := m.svc.memory.ArchiveInfo(ctx, controlInfo); err != nil || !applied {
		t.Fatalf("archive internal session: applied=%v err=%v", applied, err)
	}

	runtime := &fakeRuntimeService{live: true}
	if err := m.svc.BindRuntimeManager(fakeRuntimeManager{svc: runtime}); err != nil {
		t.Fatal(err)
	}
	counter := &sessionSummaryCountingDB{db: m.db}
	m.svc.q = sqlc.New(counter)

	out, err := sessionTool(m.svc, "list").Execute(ctx, map[string]any{"include_archived": true})
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Sessions []sessionCardResponse `json:"sessions"`
	}
	if err := json.Unmarshal([]byte(out), &response); err != nil {
		t.Fatal(err)
	}
	if counter.queries != 1 {
		t.Fatalf("summary queries = %d, want one batch for the page", counter.queries)
	}
	if len(response.Sessions) != 2 {
		t.Fatalf("sessions = %d, want 2", len(response.Sessions))
	}
	byID := make(map[string]sessionCardResponse, len(response.Sessions))
	for _, card := range response.Sessions {
		byID[card.ID] = card
	}
	card := byID[m.private]
	for _, want := range []string{
		"Earlier work established rotating refresh tokens",
		"Investigate refresh-token concurrency",
		"success: Use a single-flight lock and validate cross-node behavior",
	} {
		if !strings.Contains(card.Summary, want) {
			t.Fatalf("summary %q does not contain %q", card.Summary, want)
		}
	}
	for _, skipped := range []string{"Initial authentication question", "continue", "private chain of thought", "tool payload", "RAW-BASE64", `[{"kind"`} {
		if strings.Contains(card.Summary, skipped) {
			t.Fatalf("summary leaked skipped content %q: %q", skipped, card.Summary)
		}
	}
	if card.State != SessionStateRunning || !card.Sendable {
		t.Fatalf("chat card state/sendable = %q/%v, want running/true", card.State, card.Sendable)
	}
	if card.TurnStartedAt != "2026-08-09T01:58:12Z" {
		t.Fatalf("turn_started_at = %q, want UTC RFC3339", card.TurnStartedAt)
	}
	control := byID[m.internal]
	if control.State != SessionStateArchived || control.Sendable {
		t.Fatalf("archived task card state/sendable = %q/%v, want archived/false", control.State, control.Sendable)
	}
	if control.Summary == "" || strings.Contains(control.Summary, "internal payload") {
		t.Fatalf("non-display-only Session summary = %q, want safe non-empty fallback", control.Summary)
	}
}

func TestProjectCardsDegradesMissingConversationToTitle(t *testing.T) {
	m := newSessionMatrix(t)
	ctx := authz.WithAgentID(authz.WithUserID(context.Background(), m.owner), m.agent)
	info, err := m.svc.memory.LoadInfo(ctx, m.private)
	if err != nil {
		t.Fatal(err)
	}
	info.Title = "Rotated session"
	if _, err := m.db.Exec(ctx, `DELETE FROM ctx_conversation WHERE session_id = $1`, m.private); err != nil {
		t.Fatal(err)
	}
	authority, err := authz.NewUserAuthority(authz.UserID(m.owner), false)
	if err != nil {
		t.Fatal(err)
	}
	access, err := m.svc.Begin(ctx, authority)
	if err != nil {
		t.Fatal(err)
	}
	sessionInfo, err := agentsession.InfoFromRecord(info)
	if err != nil {
		t.Fatal(err)
	}
	cards, err := access.projectCards(ctx, []agentsession.Info{sessionInfo})
	if err != nil {
		t.Fatalf("projectCards: %v", err)
	}
	if len(cards) != 1 || cards[0].Summary != info.Title {
		t.Fatalf("cards = %#v, want title-only fallback", cards)
	}
}

func TestSessionSummaryIsBoundedAndNeverExplainsWithIntermediateEvents(t *testing.T) {
	source := sqlc.ListConversationSummarySourceBySessionIDsRow{
		HasMessages:       true,
		Background:        strings.Repeat("b", maxSessionCardSummaryBytes),
		LastUserMessage:   strings.Repeat("u", maxSessionCardSummaryBytes),
		LastAssistantText: strings.Repeat("a", maxSessionCardSummaryBytes),
	}
	if got := deriveSessionSummary("", "success", source); len(got) > maxSessionCardSummaryBytes {
		t.Fatalf("summary bytes = %d, want <= %d", len(got), maxSessionCardSummaryBytes)
	}

	fallback := deriveSessionSummary("", "", sqlc.ListConversationSummarySourceBySessionIDsRow{HasMessages: true})
	if fallback == "" {
		t.Fatal("session with only non-display message rows received an empty summary")
	}
}

func TestSessionCardTitleTruncationRemainsByteBoundedForUnicode(t *testing.T) {
	got := summaryExcerpt(strings.Repeat("界", agentsession.MaxTitleBytes), maxSessionCardTitleBytes)
	if len(got) > agentsession.MaxTitleBytes {
		t.Fatalf("card title bytes = %d, want <= %d", len(got), agentsession.MaxTitleBytes)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("card title is invalid UTF-8: %q", got)
	}
}
