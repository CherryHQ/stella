package access

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/agent"
	delegatetool "github.com/CherryHQ/stella/internal/agent/delegate"
	agentsession "github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/lcm"
	"github.com/CherryHQ/stella/internal/memory/memorytest"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	"github.com/CherryHQ/stella/pkg/tools"
)

type staticSessionSearcher struct {
	results []memory.SearchResult
	err     error
}

type recallBatchCountingDB struct {
	db      sqlc.DBTX
	queries map[string]int
}

func (d *recallBatchCountingDB) Exec(ctx context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	return d.db.Exec(ctx, query, args...)
}

func (d *recallBatchCountingDB) Query(ctx context.Context, query string, args ...any) (pgx.Rows, error) {
	for _, name := range []string{"ListConversationsForRecallAccess", "ListRecallMessageByIDs", "ListRecallSummaryByIDs", "GetConversationForSessionAccess", "GetMessage", "GetSummary"} {
		if strings.Contains(query, "-- name: "+name+" ") {
			d.queries[name]++
		}
	}
	return d.db.Query(ctx, query, args...)
}

func (d *recallBatchCountingDB) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	return d.db.QueryRow(ctx, query, args...)
}

// sessionOnlyProvider intentionally forwards Provider + SessionManager but not
// Searcher, so tracing cannot manufacture an optional search capability.
type sessionOnlyProvider struct {
	inner *memorytest.Fake
}

func (p *sessionOnlyProvider) Name() string { return p.inner.Name() }
func (p *sessionOnlyProvider) Bootstrap(ctx context.Context, session memory.Session) error {
	return p.inner.Bootstrap(ctx, session)
}

func (p *sessionOnlyProvider) Append(ctx context.Context, session memory.Session, msgs ...ai.Message) error {
	return p.inner.Append(ctx, session, msgs...)
}

func (p *sessionOnlyProvider) Assemble(ctx context.Context, session memory.Session, budget, freshTail int) ([]ai.Message, error) {
	return p.inner.Assemble(ctx, session, budget, freshTail)
}

func (p *sessionOnlyProvider) Stats(ctx context.Context, session memory.Session) (memory.SessionStats, error) {
	return p.inner.Stats(ctx, session)
}
func (p *sessionOnlyProvider) Close() error { return p.inner.Close() }
func (p *sessionOnlyProvider) SaveInfo(ctx context.Context, info memory.SessionInfo) error {
	return p.inner.SaveInfo(ctx, info)
}

func (p *sessionOnlyProvider) RotateInfo(ctx context.Context, expectedSessionID string, successor memory.SessionInfo) error {
	return p.inner.RotateInfo(ctx, expectedSessionID, successor)
}

func (p *sessionOnlyProvider) TouchActiveInfo(ctx context.Context, info memory.SessionInfo) (bool, error) {
	return p.inner.TouchActiveInfo(ctx, info)
}

func (p *sessionOnlyProvider) LoadInfo(ctx context.Context, sessionID string) (memory.SessionInfo, error) {
	return p.inner.LoadInfo(ctx, sessionID)
}

func (p *sessionOnlyProvider) ListInfo(ctx context.Context, opts memory.ListOptions) ([]memory.SessionInfo, error) {
	return p.inner.ListInfo(ctx, opts)
}

func (p *sessionOnlyProvider) LoadHistory(ctx context.Context, sessionID string) ([]ai.Message, error) {
	return p.inner.LoadHistory(ctx, sessionID)
}

func (s staticSessionSearcher) Search(context.Context, memory.Session, memory.SearchQuery) ([]memory.SearchResult, error) {
	return s.results, s.err
}

func sessionWorkerAuthority(t *testing.T, owner, agent string) authz.Authority {
	t.Helper()
	authority, err := authz.NewAgentAuthority(authz.UserID(owner), authz.AgentID(agent))
	if err != nil {
		t.Fatal(err)
	}
	return authority
}

func TestSessionToolUsesRuntimeIdentityAndNewSurface(t *testing.T) {
	m := newSessionMatrix(t)
	tool := NewTool(m.svc)
	ctx := authz.WithAgentID(authz.WithUserID(context.Background(), m.owner), m.agent)

	out, err := tool.Execute(ctx, map[string]any{"action": "list", "include_archived": true})
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
		t.Fatalf("session kind leaked from list: %s", out)
	}
	if _, err := tool.Execute(ctx, map[string]any{"action": "get", "session_id": m.private}); err != nil {
		t.Fatalf("owner get: %v", err)
	}
	for _, removed := range []string{"find", "messages"} {
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
	for _, hidden := range []string{"user_id", "agent_id", "kind", "skip", "limit", "query"} {
		if _, ok := properties[hidden]; ok {
			t.Fatalf("session tool schema must not expose %s", hidden)
		}
	}
}

func TestSessionRecallTracedProviderWithoutSearcherUsesEmptyLane(t *testing.T) {
	m := newSessionMatrix(t)
	traced := memory.WithTracing(&sessionOnlyProvider{inner: memorytest.New()}, nil)
	svc, err := NewService(traced, m.db, m.svc.store, m.svc.assets, m.svc.agents)
	if err != nil {
		t.Fatalf("NewService with traced SessionManager: %v", err)
	}
	if svc.searcher != nil {
		t.Fatal("tracing manufactured a Searcher capability absent from the inner provider")
	}
	results, err := svc.SearchRecall(t.Context(), sessionWorkerAuthority(t, m.owner, m.agent), m.agent, "durable memory", 20)
	if err != nil {
		t.Fatalf("no-Searcher recall lane should degrade to empty: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("no-Searcher recall results=%#v, want empty lane", results)
	}
}

func TestSessionListBoundsUserControlledTitlesAndSerializedResult(t *testing.T) {
	m := newSessionMatrix(t)
	info, err := m.svc.memory.LoadInfo(t.Context(), m.private)
	if err != nil {
		t.Fatal(err)
	}
	info.Title = strings.Repeat("t", 200_000)
	if err := m.svc.memory.SaveInfo(t.Context(), info); err != nil {
		t.Fatal(err)
	}
	ctx := authz.WithAgentID(authz.WithUserID(t.Context(), m.owner), m.agent)
	out, err := NewTool(m.svc).Execute(ctx, map[string]any{"action": "list", "include_archived": true, "page_size": 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) > maxSessionToolSerializedResult {
		t.Fatalf("serialized session.list bytes=%d, limit=%d", len(out), maxSessionToolSerializedResult)
	}
	var list struct {
		Sessions []sessionCardResponse `json:"sessions"`
	}
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, card := range list.Sessions {
		if card.ID == m.private {
			found = true
			if len(card.Title) != maxSessionCardTitleBytes {
				t.Fatalf("bounded title bytes=%d, want %d", len(card.Title), maxSessionCardTitleBytes)
			}
		}
	}
	if !found {
		t.Fatal("bounded session card was not returned")
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

func TestConversationSessionBoundsOutputAndDrainsAfterTerminalError(t *testing.T) {
	m := newSessionMatrix(t)
	wantErr := errors.New("terminal conversation error")
	events := make([]agent.Event, 0, 102)
	for range 50 {
		events = append(events, agent.Event{Text: strings.Repeat("x", 1_024)})
	}
	events = append(events, agent.Event{Text: strings.Repeat("y", 20_000), Err: wantErr})
	// A defensive collector must still drain protocol violations after a
	// terminal event rather than leaving the producer blocked.
	for range 50 {
		events = append(events, agent.Event{Text: strings.Repeat("z", 1_024)})
	}
	done := make(chan struct{})
	runtime := &fakeRuntimeService{chatEvents: events, chatDone: done}
	m.svc.runtime = fakeRuntimeManager{svc: runtime}
	record, err := m.svc.memory.LoadInfo(t.Context(), m.private)
	if err != nil {
		t.Fatal(err)
	}
	info, err := agentsession.InfoFromRecord(record)
	if err != nil {
		t.Fatal(err)
	}

	result, err := runConversationSession(t.Context(), m.svc, info, "continue")
	if !errors.Is(err, wantErr) {
		t.Fatalf("terminal error=%v, want %v", err, wantErr)
	}
	response, ok := result.(sessionRunResponse)
	if !ok {
		t.Fatalf("partial result type=%T, want sessionRunResponse", result)
	}
	if len(response.Reply) > maxSessionToolResultText || !response.ReplyTruncated {
		t.Fatalf("bounded conversation reply bytes=%d truncated=%v", len(response.Reply), response.ReplyTruncated)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("conversation producer remained blocked after terminal error")
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

func TestSessionRecallSearchUsesLCMRetrievalAndExactPrincipalAgentScope(t *testing.T) {
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

	authority := sessionWorkerAuthority(t, m.owner, m.agent)
	results, err := m.svc.SearchRecall(ctx, authority, m.agent, "rotatedtokenneedle", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].SessionID != m.private || results[0].Reference.Kind != "message" {
		t.Fatalf("scoped recall results=%#v, want only %s", results, m.private)
	}
	if !strings.Contains(results[0].Content, "rotatedtokenneedle") || results[0].Reference.ID == "" {
		t.Fatalf("recall result lacks content/ref: %#v", results[0])
	}
	doc, err := m.svc.ReadRecall(ctx, authority, m.agent, results[0].Reference, 4_000)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(doc.Content, "rotatedtokenneedle") || doc.SessionID != m.private {
		t.Fatalf("read recall missed matching message: %#v", doc)
	}

	foreignAuthority := sessionWorkerAuthority(t, m.other, m.agent)
	foreign, err := m.svc.SearchRecall(ctx, foreignAuthority, m.agent, "rotatedtokenneedle", 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range foreign {
		if result.SessionID == m.private {
			t.Fatalf("cross-user recall exposed owner session: %#v", foreign)
		}
	}
	if _, err := m.svc.ReadRecall(ctx, foreignAuthority, m.agent, results[0].Reference, 4_000); err == nil || !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign caller read a forged owner ref: %v", err)
	}
}

func TestSessionRecallDropsDeletedSearchSource(t *testing.T) {
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

	authority := sessionWorkerAuthority(t, m.owner, m.agent)
	results, err := m.svc.SearchRecall(ctx, authority, m.agent, "stale anchor", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatalf("deleted recall source was returned: %#v", results)
	}
}

func TestSessionRecallBatchesAuthorizationAndResourceVerification(t *testing.T) {
	m := newSessionMatrix(t)
	provider, err := lcm.New(m.db.(*pgxpool.Pool), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, sessionID := range []string{m.private, m.internal} {
		session := memory.Session{ID: sessionID, UserID: m.owner, AgentID: m.agent}
		for i := range 4 {
			appendTranscript(t, provider, session, ai.UserMessage{Content: fmt.Sprintf("batched recall %s %d", sessionID, i)})
		}
	}
	var rows []sqlc.CtxMessage
	for _, sessionID := range []string{m.private, m.internal} {
		var conversationID string
		if err := m.db.QueryRow(t.Context(), `SELECT id FROM ctx_conversation WHERE session_id = $1`, sessionID).Scan(&conversationID); err != nil {
			t.Fatal(err)
		}
		conversationRows, err := sqlc.New(m.db).GetMessagesByConversation(t.Context(), conversationID)
		if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, conversationRows...)
	}
	hits := make([]memory.SearchResult, 0, len(rows)+1)
	for _, row := range rows {
		var sessionID string
		if err := m.db.QueryRow(t.Context(), `SELECT session_id FROM ctx_conversation WHERE id = $1`, row.ConversationID).Scan(&sessionID); err != nil {
			t.Fatal(err)
		}
		hits = append(hits, memory.SearchResult{SourceType: "message", SourceID: row.ID, SessionID: sessionID, Content: row.Content})
	}
	hits = append(hits, memory.SearchResult{SourceType: "message", SourceID: uuid.NewString(), SessionID: m.private, Content: "stale"})
	m.svc.searcher = staticSessionSearcher{results: hits}
	counter := &recallBatchCountingDB{db: m.db, queries: make(map[string]int)}
	m.svc.q = sqlc.New(counter)

	results, err := m.svc.SearchRecall(t.Context(), sessionWorkerAuthority(t, m.owner, m.agent), m.agent, "batched", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != len(rows) {
		t.Fatalf("authorized results=%d, want %d live rows", len(results), len(rows))
	}
	if counter.queries["ListConversationsForRecallAccess"] != 1 || counter.queries["ListRecallMessageByIDs"] != 1 {
		t.Fatalf("recall batch queries=%v, want one Session batch and one message batch", counter.queries)
	}
	for _, forbidden := range []string{"GetConversationForSessionAccess", "GetMessage", "GetSummary"} {
		if counter.queries[forbidden] != 0 {
			t.Fatalf("recall regressed to per-hit %s queries: %v", forbidden, counter.queries)
		}
	}
}

func TestSessionRecallAllowsDurableOnlyProvidersWithoutWeakeningPolicy(t *testing.T) {
	m := newSessionMatrix(t)
	m.svc.searcher = nil

	results, err := m.svc.SearchRecall(t.Context(), sessionWorkerAuthority(t, m.owner, m.agent), m.agent, "durable memory", 20)
	if err != nil || len(results) != 0 {
		t.Fatalf("authorized provider without transcript search: results=%#v err=%v", results, err)
	}
	groupAuthority, err := authz.NewGroupAgentAuthority(authz.GroupID(m.group), authz.AgentID(m.agent))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.svc.SearchRecall(t.Context(), groupAuthority, m.agent, "private memory", 20); !errors.Is(err, ErrNotFound) {
		t.Fatalf("provider without transcript search weakened group policy: %v", err)
	}
}

func TestSessionRecallSummaryPreservesExpansionAndRejectsCrossConversationLineage(t *testing.T) {
	m := newSessionMatrix(t)
	provider, err := lcm.New(m.db.(*pgxpool.Pool), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	appendTranscript(t, provider, memory.Session{ID: m.private, UserID: m.owner, AgentID: m.agent},
		ai.UserMessage{Content: "source message retained below the summary"},
	)
	appendTranscript(t, provider, memory.Session{ID: "foreign-summary-session", UserID: m.other, AgentID: m.agent},
		ai.UserMessage{Content: "foreign source must never expand"},
	)
	var conversationID, messageID, foreignConversationID, foreignMessageID string
	if err := m.db.QueryRow(ctx, `
		SELECT c.id, m.id FROM ctx_conversation c
		JOIN ctx_message m ON m.conversation_id = c.id
		WHERE c.session_id = $1 ORDER BY m.seq DESC LIMIT 1`, m.private).Scan(&conversationID, &messageID); err != nil {
		t.Fatal(err)
	}
	if err := m.db.QueryRow(ctx, `
		SELECT c.id, m.id FROM ctx_conversation c
		JOIN ctx_message m ON m.conversation_id = c.id
		WHERE c.session_id = $1 ORDER BY m.seq DESC LIMIT 1`, "foreign-summary-session").Scan(&foreignConversationID, &foreignMessageID); err != nil {
		t.Fatal(err)
	}
	q := sqlc.New(m.db)
	if err := q.CreateSummary(ctx, sqlc.CreateSummaryParams{
		ID: "summary-root", ConversationID: conversationID, Kind: "leaf", Content: "bounded summary", TokenCount: 2, DescendantCount: 1,
		ContainsNonPrincipalInput: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := q.LinkSummaryToMessage(ctx, sqlc.LinkSummaryToMessageParams{SummaryID: "summary-root", MessageID: messageID, Ordinal: 1}); err != nil {
		t.Fatal(err)
	}
	authority := sessionWorkerAuthority(t, m.owner, m.agent)
	ref := memory.RecallReference{Kind: "summary", ID: "summary-root", SessionID: m.private}
	doc, err := m.svc.ReadRecall(ctx, authority, m.agent, ref, 4_000)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Summary == nil || len(doc.Summary.Expanded) != 1 || doc.Summary.Expanded[0].Reference.Kind != "message" || !strings.Contains(doc.Summary.Expanded[0].Content, "retained") {
		t.Fatalf("summary expansion lost LCM detail: %#v", doc)
	}
	if doc.Authority != "information_only" {
		t.Fatalf("summary read lost non-principal authority: %#v", doc)
	}
	if err := q.CreateSummary(ctx, sqlc.CreateSummaryParams{
		ID: "summary-child", ConversationID: conversationID, Kind: "leaf", Depth: 0, Content: "child summary", TokenCount: 2,
		ContainsNonPrincipalInput: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := q.CreateSummary(ctx, sqlc.CreateSummaryParams{
		ID: "summary-condensed", ConversationID: conversationID, Kind: "condensed", Depth: 1, Content: "condensed root", TokenCount: 2,
		ContainsNonPrincipalInput: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := q.LinkSummaryToParent(ctx, sqlc.LinkSummaryToParentParams{
		SummaryID: "summary-condensed", ParentSummaryID: "summary-child", Ordinal: 1,
	}); err != nil {
		t.Fatal(err)
	}
	condensed, err := m.svc.ReadRecall(ctx, authority, m.agent, memory.RecallReference{Kind: "summary", ID: "summary-condensed", SessionID: m.private}, 4_000)
	if err != nil {
		t.Fatal(err)
	}
	if condensed.Summary == nil || len(condensed.Summary.Parents) != 0 || len(condensed.Summary.Children) != 1 || condensed.Summary.Children[0].ID != "summary-child" || len(condensed.Summary.Expanded) != 1 || condensed.Summary.Expanded[0].Kind != "leaf" || condensed.Summary.Expanded[0].Depth == nil || *condensed.Summary.Expanded[0].Depth != 0 {
		t.Fatalf("condensed expansion lost child kind/depth: %#v", condensed)
	}
	if condensed.Authority != "information_only" || condensed.Summary.Expanded[0].Authority != "information_only" {
		t.Fatalf("summary read/expansion lost non-principal authority: %#v", condensed)
	}
	child, err := m.svc.ReadRecall(ctx, authority, m.agent, memory.RecallReference{Kind: "summary", ID: "summary-child", SessionID: m.private}, 4_000)
	if err != nil {
		t.Fatal(err)
	}
	if child.Summary == nil || len(child.Summary.Parents) != 1 || child.Summary.Parents[0].ID != "summary-condensed" || len(child.Summary.Children) != 0 {
		t.Fatalf("summary lineage direction was reversed: %#v", child)
	}
	access, err := m.svc.Begin(ctx, authority)
	if err != nil {
		t.Fatal(err)
	}
	stats, err := access.ContextStats(ctx, m.agent, m.private)
	if err != nil {
		t.Fatal(err)
	}
	if stats.MessageCount != 1 || stats.SummaryCount != 3 || stats.SummaryDepth != 1 || stats.SourceTokenCount == 0 || stats.ActiveTokenCount == 0 || stats.OldestAt == nil || stats.NewestAt == nil {
		t.Fatalf("context stats did not include summary/time metadata: %#v", stats)
	}

	if err := q.CreateSummary(ctx, sqlc.CreateSummaryParams{
		ID: "summary-cross-message", ConversationID: conversationID, Kind: "leaf", Content: "unsafe message link", TokenCount: 2,
	}); err != nil {
		t.Fatal(err)
	}
	if err := q.LinkSummaryToMessage(ctx, sqlc.LinkSummaryToMessageParams{SummaryID: "summary-cross-message", MessageID: foreignMessageID, Ordinal: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.svc.ReadRecall(ctx, authority, m.agent, memory.RecallReference{Kind: "summary", ID: "summary-cross-message", SessionID: m.private}, 4_000); err == nil || !strings.Contains(err.Error(), "crosses conversation boundary") {
		t.Fatalf("cross-conversation summary message was not rejected: %v", err)
	}

	if err := q.CreateSummary(ctx, sqlc.CreateSummaryParams{
		ID: "summary-foreign", ConversationID: foreignConversationID, Kind: "leaf", Content: "foreign summary", TokenCount: 2,
	}); err != nil {
		t.Fatal(err)
	}
	// A foreign constituent must make a container read fail closed.
	if err := q.LinkSummaryToParent(ctx, sqlc.LinkSummaryToParentParams{
		SummaryID: "summary-root", ParentSummaryID: "summary-foreign", Ordinal: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.svc.ReadRecall(ctx, authority, m.agent, ref, 4_000); err == nil || !strings.Contains(err.Error(), "crosses conversation boundary") {
		t.Fatalf("cross-conversation summary child was not rejected: %v", err)
	}
	// A foreign container must also make its local constituent fail closed.
	if err := q.LinkSummaryToParent(ctx, sqlc.LinkSummaryToParentParams{
		SummaryID: "summary-foreign", ParentSummaryID: "summary-child", Ordinal: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.svc.ReadRecall(ctx, authority, m.agent, memory.RecallReference{Kind: "summary", ID: "summary-child", SessionID: m.private}, 4_000); err == nil || !strings.Contains(err.Error(), "crosses conversation boundary") {
		t.Fatalf("cross-conversation summary parent was not rejected: %v", err)
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
	if compact.ContextStats.MessageCount != 6 || compact.ContextStats.TokenCount == 0 || compact.ContextStats.ActiveTokenCount == 0 || compact.ContextStats.SummaryCount != 0 || compact.ContextStats.OldestAt == "" || compact.ContextStats.NewestAt == "" {
		t.Fatalf("missing context stats: %#v", compact.ContextStats)
	}
	initialCursor, err := decodeTranscriptCursor(compact.TranscriptCursor, m.private)
	if err != nil || initialCursor.AnchorSeq == 0 || initialCursor.SnapshotSeq != initialCursor.AnchorSeq {
		t.Fatalf("initial transcript cursor is not snapshot-anchored: cursor=%#v err=%v", initialCursor, err)
	}
	// A turn appended after the compact preview must not move the result set
	// underneath the cursor's offset-based continuation. A later assistant row
	// must not grow the turn that was only partially visible at the snapshot.
	appendTranscript(t, provider, memory.Session{ID: m.private, UserID: m.owner, AgentID: m.agent},
		ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "late continuation after snapshot"}}},
	)
	appendTranscript(t, provider, memory.Session{ID: m.private, UserID: m.owner, AgentID: m.agent},
		ai.UserMessage{Content: "third request after snapshot"},
		ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "third answer after snapshot"}}},
	)

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
	latestMessages := page.Transcript.Turns[0].Messages
	if len(latestMessages) != 2 || latestMessages[1].Content != "second answer" {
		t.Fatalf("post-snapshot row grew the anchored latest turn: %#v", latestMessages)
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
	if page.Transcript.HasMore {
		t.Fatalf("anchored pagination did not exhaust the two-turn snapshot: %#v", page.Transcript)
	}
	if messages[1].Type != "tool_call" || messages[2].Type != "tool_result" || messages[1].ToolCallID != messages[2].ToolCallID {
		t.Fatalf("tool call/result pairing lost: %#v", messages)
	}
	if !messages[2].Truncated || len(messages[2].Content) != maxSessionToolOutputText {
		t.Fatalf("oversized tool output not bounded: len=%d truncated=%v", len(messages[2].Content), messages[2].Truncated)
	}
}

func TestSessionGetBoundsSerializedMetadataAndKeepsToolPairs(t *testing.T) {
	m := newSessionMatrix(t)
	provider, err := lcm.New(m.db.(*pgxpool.Pool), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := authz.WithAgentID(authz.WithUserID(context.Background(), m.owner), m.agent)
	messages := []ai.Message{ai.UserMessage{Content: "run the bulk tool plan"}}
	toolCalls := make([]ai.ContentBlock, 0, 400)
	for i := range 400 {
		id := fmt.Sprintf("bulk-call-%03d", i)
		toolCalls = append(toolCalls, ai.ToolCall{ID: id, Name: "bulk_tool", Arguments: map[string]any{"index": i}})
	}
	messages = append(messages, ai.AssistantMessage{Content: toolCalls})
	for i := range 400 {
		id := fmt.Sprintf("bulk-call-%03d", i)
		messages = append(messages, ai.ToolResultMessage{ToolCallID: id, ToolName: "bulk_tool", Content: []ai.ContentBlock{ai.TextContent{Text: "ok"}}})
	}
	messages = append(messages, ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "bulk work complete"}}})
	appendTranscript(t, provider, memory.Session{ID: m.private, UserID: m.owner, AgentID: m.agent}, messages...)

	tool := NewTool(m.svc)
	compactOut, err := tool.Execute(ctx, map[string]any{"action": "get", "session_id": m.private})
	if err != nil {
		t.Fatal(err)
	}
	var compact sessionGetResponse
	if err := json.Unmarshal([]byte(compactOut), &compact); err != nil {
		t.Fatal(err)
	}
	pageOut, err := tool.Execute(ctx, map[string]any{
		"action": "get", "session_id": m.private, "cursor": compact.TranscriptCursor, "transcript_page_size": 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(pageOut) > maxSessionToolSerializedResult {
		t.Fatalf("serialized session.get bytes=%d, limit=%d", len(pageOut), maxSessionToolSerializedResult)
	}
	var page sessionGetResponse
	if err := json.Unmarshal([]byte(pageOut), &page); err != nil {
		t.Fatal(err)
	}
	if page.Transcript == nil || page.Transcript.Omitted == nil || page.Transcript.Omitted.MessageCount == 0 || !page.Transcript.Omitted.Permanent {
		t.Fatalf("missing truthful omission marker: %#v", page.Transcript)
	}
	if page.Transcript.HasMore || page.Transcript.NextCursor != "" {
		t.Fatalf("single oversized turn exposed a misleading continuation: %#v", page.Transcript)
	}
	seenCalls := make(map[string]bool)
	seenResults := make(map[string]bool)
	for _, turn := range page.Transcript.Turns {
		for _, message := range turn.Messages {
			switch message.Type {
			case "tool_call":
				seenCalls[message.ToolCallID] = true
			case "tool_result":
				seenResults[message.ToolCallID] = true
			}
		}
	}
	for id := range seenCalls {
		if !seenResults[id] {
			t.Fatalf("emitted tool call %q without its result", id)
		}
	}
	for id := range seenResults {
		if !seenCalls[id] {
			t.Fatalf("emitted tool result %q without its call", id)
		}
	}
	serialized, err := json.Marshal(page.Transcript.Omitted)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(serialized), "cursor") {
		t.Fatalf("permanent omission advertised a replay cursor: %s", serialized)
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
		"action": "list", "page_token": tools.OffsetToken(math.MaxInt32),
	}); err == nil || !strings.Contains(err.Error(), "invalid pagination") {
		t.Fatalf("large list offset error=%v, want invalid pagination", err)
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
