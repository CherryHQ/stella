package lcm_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/lcm"
	"github.com/CherryHQ/stella/pkg/ai"
)

// newSearchTestEnv builds a provider on a raw db handle so tests can both use
// the public Append path and reach behind it (insert summaries, delete rows).
func newSearchTestEnv(t *testing.T, suffix string) (*pgxpool.Pool, memory.Provider, memory.Session) {
	t.Helper()
	db := newLCMTestDB(t)
	t.Cleanup(func() { db.Close() })

	p, err := lcm.New(db, nil, nil)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })

	sess := newLCMTestSession(suffix)
	if err := p.Bootstrap(context.Background(), sess); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	return db, p, sess
}

func conversationID(t *testing.T, db *pgxpool.Pool, sessionID string) string {
	t.Helper()
	var id string
	if err := db.QueryRow(context.Background(), `SELECT id FROM ctx_conversation WHERE session_id = $1`, sessionID).Scan(&id); err != nil {
		t.Fatalf("conversation id: %v", err)
	}
	return id
}

func insertSummary(t *testing.T, db *pgxpool.Pool, convID, id, content string) {
	t.Helper()
	_, err := db.Exec(context.Background(), `INSERT INTO ctx_summary (id, conversation_id, kind, depth, content, token_count)
		VALUES ($1, $2, 'leaf', 0, $3, 10)`, id, convID, content)
	if err != nil {
		t.Fatalf("insert summary: %v", err)
	}
}

func insertSummaryAt(t *testing.T, db *pgxpool.Pool, convID, id, content, latestAt string) {
	t.Helper()
	_, err := db.Exec(context.Background(), `INSERT INTO ctx_summary (id, conversation_id, kind, depth, content, token_count, latest_at)
		VALUES ($1, $2, 'leaf', 0, $3, 10, $4)`, id, convID, content, latestAt)
	if err != nil {
		t.Fatalf("insert summary: %v", err)
	}
}

func messageIDs(t *testing.T, db *pgxpool.Pool, convID string) []string {
	t.Helper()
	rows, err := db.Query(context.Background(), `SELECT id FROM ctx_message WHERE conversation_id = $1 ORDER BY seq ASC`, convID)
	if err != nil {
		t.Fatalf("query message ids: %v", err)
	}
	defer func() { rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan message id: %v", err)
		}
		ids = append(ids, id)
	}
	return ids
}

func linkSummaryMessage(t *testing.T, db *pgxpool.Pool, summaryID, messageID string, ordinal int) {
	t.Helper()
	if _, err := db.Exec(context.Background(), `INSERT INTO ctx_summary_message (summary_id, message_id, ordinal) VALUES ($1, $2, $3)`,
		summaryID, messageID, ordinal); err != nil {
		t.Fatalf("link summary message: %v", err)
	}
}

func parseTestTime(t *testing.T, s string) time.Time {
	t.Helper()
	parsed, err := time.Parse("2006-01-02 15:04:05", s)
	if err != nil {
		t.Fatalf("parse time %q: %v", s, err)
	}
	return parsed
}

func appendUser(t *testing.T, p memory.Provider, sess memory.Session, contents ...string) {
	t.Helper()
	for _, c := range contents {
		if err := p.Append(context.Background(), sess, ai.UserMessage{Content: c}); err != nil {
			t.Fatalf("append %q: %v", c, err)
		}
	}
}

func runSearch(t *testing.T, p memory.Provider, sess memory.Session, q memory.SearchQuery) []memory.SearchResult {
	t.Helper()
	results, err := p.(memory.Searcher).Search(context.Background(), sess, q)
	if err != nil {
		t.Fatalf("search %q: %v", q.Text, err)
	}
	return results
}

func TestSearch_MessagesRankedByBM25(t *testing.T) {
	_, p, sess := newSearchTestEnv(t, "fts-rank")
	appendUser(t, p, sess,
		"alpha beaver alpha strongdoc",
		"alpha weakdoc padding words",
		"completely irrelevant content",
	)

	results := runSearch(t, p, sess, memory.SearchQuery{Text: "alpha beaver", Scope: memory.SearchScopeMessages})
	if len(results) != 2 {
		t.Fatalf("expected 2 hits, got %d: %+v", len(results), results)
	}
	if !strings.Contains(results[0].Content, "strongdoc") {
		t.Errorf("expected strong doc ranked first, got %q", results[0].Content)
	}
	if results[0].Score <= results[1].Score {
		t.Errorf("expected descending scores, got %v then %v", results[0].Score, results[1].Score)
	}
	// pg_search snippet highlights matched terms with <b></b>.
	if !strings.Contains(results[0].Content, "<b>alpha</b>") {
		t.Errorf("expected snippet highlighting, got %q", results[0].Content)
	}
}

func TestSearch_SummariesReturnHits(t *testing.T) {
	db, p, sess := newSearchTestEnv(t, "fts-sum")
	convID := conversationID(t, db, sess.ID)
	insertSummary(t, db, convID, "sum-fts-1", "quarterly zebra report discussed in detail")

	results := runSearch(t, p, sess, memory.SearchQuery{Text: "zebra", Scope: memory.SearchScopeSummaries})
	if len(results) != 1 {
		t.Fatalf("expected 1 hit, got %d", len(results))
	}
	if results[0].SourceType != "summary" || results[0].SourceID != "sum-fts-1" {
		t.Errorf("unexpected hit: %+v", results[0])
	}
}

func TestSearch_ScopeIsolation(t *testing.T) {
	db, p, sess := newSearchTestEnv(t, "fts-scope")
	convID := conversationID(t, db, sess.ID)
	appendUser(t, p, sess, "gamma in a message")
	insertSummary(t, db, convID, "sum-fts-2", "gamma in a summary")

	msgs := runSearch(t, p, sess, memory.SearchQuery{Text: "gamma", Scope: memory.SearchScopeMessages})
	for _, r := range msgs {
		if r.SourceType != "message" {
			t.Errorf("messages scope returned %q hit", r.SourceType)
		}
	}
	if len(msgs) != 1 {
		t.Errorf("messages scope: expected 1 hit, got %d", len(msgs))
	}

	sums := runSearch(t, p, sess, memory.SearchQuery{Text: "gamma", Scope: memory.SearchScopeSummaries})
	for _, r := range sums {
		if r.SourceType != "summary" {
			t.Errorf("summaries scope returned %q hit", r.SourceType)
		}
	}
	if len(sums) != 1 {
		t.Errorf("summaries scope: expected 1 hit, got %d", len(sums))
	}
}

func TestSearch_BothMergesAndRanks(t *testing.T) {
	// Merging two independent BM25 indexes can't compare their raw scores (IDF is
	// per-index), so search fuses by Reciprocal Rank Fusion: each source's rank-0
	// hit outranks both sources' rank-1 hits. Give messages and summaries each a
	// strong (rank 0) and a weak (rank 1) "delta" hit, then assert the two strong
	// hits land first and come from different sources — RRF lifts each index's
	// best hit, it does not just sort everything by raw score.
	db, p, sess := newSearchTestEnv(t, "fts-both")
	convID := conversationID(t, db, sess.ID)
	appendUser(t, p, sess, "delta delta delta strong message focus")
	appendUser(t, p, sess, "delta weak among other padding words here")
	insertSummary(t, db, convID, "sum-strong", "delta delta delta strong summary focus")
	insertSummary(t, db, convID, "sum-weak", "delta weak among other padding words")

	results := runSearch(t, p, sess, memory.SearchQuery{Text: "delta", Scope: memory.SearchScopeBoth})
	if len(results) != 4 {
		t.Fatalf("expected 4 hits across sources, got %d: %+v", len(results), results)
	}
	for i, r := range results[:2] {
		if !strings.Contains(r.Content, "strong") {
			t.Errorf("RRF: result[%d] should be a rank-0 (strong) hit, got %q", i, r.Content)
		}
	}
	if results[0].SourceType == results[1].SourceType {
		t.Errorf("RRF should interleave both sources' top hits, got two %q", results[0].SourceType)
	}
	for i, r := range results[2:] {
		if !strings.Contains(r.Content, "weak") {
			t.Errorf("RRF: result[%d] should be a rank-1 (weak) hit, got %q", i+2, r.Content)
		}
	}
}

func TestSearch_BothRespectsLimit(t *testing.T) {
	db, p, sess := newSearchTestEnv(t, "fts-limit")
	convID := conversationID(t, db, sess.ID)
	for i := range 3 {
		appendUser(t, p, sess, fmt.Sprintf("epsilon message number %d", i))
		insertSummary(t, db, convID, fmt.Sprintf("sum-fts-lim-%d", i), fmt.Sprintf("epsilon summary number %d", i))
	}

	results := runSearch(t, p, sess, memory.SearchQuery{Text: "epsilon", Scope: memory.SearchScopeBoth, Limit: 4})
	if len(results) != 4 {
		t.Fatalf("expected merged results capped at limit 4, got %d", len(results))
	}
}

func TestSearch_DeleteRemovesFTSHits(t *testing.T) {
	db, p, sess := newSearchTestEnv(t, "fts-del")
	appendUser(t, p, sess, "ephemeral xylophone note")

	if got := runSearch(t, p, sess, memory.SearchQuery{Text: "xylophone"}); len(got) != 1 {
		t.Fatalf("expected 1 hit before delete, got %d", len(got))
	}

	convID := conversationID(t, db, sess.ID)
	// ctx_item restricts message deletes, so clear the pointer first.
	if _, err := db.Exec(context.Background(), `DELETE FROM ctx_item WHERE conversation_id = $1`, convID); err != nil {
		t.Fatalf("delete items: %v", err)
	}
	if _, err := db.Exec(context.Background(), `DELETE FROM ctx_message WHERE conversation_id = $1`, convID); err != nil {
		t.Fatalf("delete messages: %v", err)
	}

	if got := runSearch(t, p, sess, memory.SearchQuery{Text: "xylophone"}); len(got) != 0 {
		t.Fatalf("expected 0 hits after delete, got %d: %+v", len(got), got)
	}
}

func TestSearch_SpansSessionsOfSameUserAgent(t *testing.T) {
	// Both sessions share the same (user_id, agent_id), so memory recall must
	// span both — searching from session A still surfaces what was said in
	// session B.
	_, p, sess := newSearchTestEnv(t, "fts-span-a")
	appendUser(t, p, sess, "shared quokka keyword in convA")

	other := newLCMTestSession("fts-span-b")
	if err := p.Bootstrap(context.Background(), other); err != nil {
		t.Fatalf("bootstrap other: %v", err)
	}
	appendUser(t, p, other, "shared quokka keyword in convB")

	results := runSearch(t, p, sess, memory.SearchQuery{Text: "quokka"})
	if len(results) != 2 {
		t.Fatalf("expected hits from both sessions, got %d: %+v", len(results), results)
	}
	// Provenance: each hit carries its origin session so the agent can trace it.
	seen := map[string]bool{}
	for _, r := range results {
		if r.SessionID == "" {
			t.Errorf("hit missing origin session: %+v", r)
		}
		seen[r.SessionID] = true
	}
	if !seen[sess.ID] || !seen[other.ID] {
		t.Errorf("expected hits from both %q and %q, saw %v", sess.ID, other.ID, seen)
	}
}

func TestSearch_IsolatesOtherUsersAndAgents(t *testing.T) {
	_, p, sess := newSearchTestEnv(t, "fts-iso-self")
	appendUser(t, p, sess, "private wombat keyword for owner")

	// Same DB, different user and different agent: neither may leak into the
	// owner's recall.
	otherUser := newLCMTestSession("fts-iso-user")
	otherUser.UserID = testOtherUserID
	if err := p.Bootstrap(context.Background(), otherUser); err != nil {
		t.Fatalf("bootstrap other user: %v", err)
	}
	appendUser(t, p, otherUser, "private wombat keyword for stranger")

	otherAgent := newLCMTestSession("fts-iso-agent")
	otherAgent.AgentID = "other"
	if err := p.Bootstrap(context.Background(), otherAgent); err != nil {
		t.Fatalf("bootstrap other agent: %v", err)
	}
	appendUser(t, p, otherAgent, "private wombat keyword for other agent")

	results := runSearch(t, p, sess, memory.SearchQuery{Text: "wombat"})
	if len(results) != 1 {
		t.Fatalf("expected 1 hit scoped to owner, got %d: %+v", len(results), results)
	}
	if !strings.Contains(results[0].Content, "owner") {
		t.Errorf("hit leaked across user/agent boundary: %q", results[0].Content)
	}
}

func TestSearch_SpecialCharactersDoNotError(t *testing.T) {
	_, p, sess := newSearchTestEnv(t, "fts-special")
	appendUser(t, p, sess, "function parse_config returns errors")

	for _, q := range []string{
		`parse_config*`,
		`"parse_config"`,
		`(parse_config) -errors`,
		`parse_config: NOT errors`,
	} {
		results := runSearch(t, p, sess, memory.SearchQuery{Text: q})
		if len(results) == 0 {
			t.Errorf("query %q: expected hits, got none", q)
		}
	}

	// No extractable tokens: empty results, no error.
	for _, q := range []string{"", `***`, `"(" - :`} {
		if got := runSearch(t, p, sess, memory.SearchQuery{Text: q}); len(got) != 0 {
			t.Errorf("query %q: expected empty results, got %d", q, len(got))
		}
	}
}

func TestSearch_PunctuationInQueryDoesNotPollute(t *testing.T) {
	// jieba emits punctuation as its own token, so "alpha, beaver" would otherwise
	// match every row containing a comma. normalizeQuery folds the comma to a
	// space, so only rows with the real terms come back — the comma-only row must
	// not appear.
	_, p, sess := newSearchTestEnv(t, "fts-punct")
	appendUser(t, p, sess,
		"alpha beaver strong match",
		"hello, world, unrelated commas only",
	)

	results := runSearch(t, p, sess, memory.SearchQuery{Text: "alpha, beaver", Scope: memory.SearchScopeMessages})
	if len(results) != 1 {
		t.Fatalf("expected 1 hit (comma-only row must not pollute), got %d: %+v", len(results), results)
	}
	if !strings.Contains(results[0].Content, "alpha") {
		t.Errorf("expected the alpha/beaver row, got %q", results[0].Content)
	}
}

func TestSearch_CJKMatch(t *testing.T) {
	_, p, sess := newSearchTestEnv(t, "fts-cjk")
	appendUser(t, p, sess, "今天讨论了部署方案的细节和时间表")

	// pg_search tokenizes CJK with jieba word segmentation, so a CJK query is a
	// real BM25 hit (positive score), not a degraded substring fallback.
	results := runSearch(t, p, sess, memory.SearchQuery{Text: "部署方案", Scope: memory.SearchScopeMessages})
	if len(results) != 1 {
		t.Fatalf("expected 1 hit for 部署方案, got %d", len(results))
	}
	if results[0].Score <= 0 {
		t.Errorf("CJK BM25 hit should carry a positive score, got %v", results[0].Score)
	}
}

func TestSearch_ShortCJKQueryMatches(t *testing.T) {
	db, p, sess := newSearchTestEnv(t, "fts-short")
	convID := conversationID(t, db, sess.ID)
	appendUser(t, p, sess, "今天讨论了部署方案的细节")
	insertSummary(t, db, convID, "sum-fts-short", "总结：部署流程已经确定")

	// A short 2-char CJK token is segmented by jieba and matches via BM25 in both
	// scopes, with a positive score and no fallback tier.
	results := runSearch(t, p, sess, memory.SearchQuery{Text: "部署", Scope: memory.SearchScopeBoth})
	if len(results) != 2 {
		t.Fatalf("expected 2 hits for 部署, got %d: %+v", len(results), results)
	}
	for _, r := range results {
		if r.Score <= 0 {
			t.Errorf("BM25 hit should carry a positive score, got %v", r.Score)
		}
	}

	msgs := runSearch(t, p, sess, memory.SearchQuery{Text: "部署", Scope: memory.SearchScopeMessages})
	if len(msgs) != 1 || msgs[0].SourceType != "message" {
		t.Errorf("messages scope: expected 1 message hit, got %+v", msgs)
	}
}

func TestSearch_CJKSpansSessionsButIsolatesUsers(t *testing.T) {
	_, p, sess := newSearchTestEnv(t, "fts-like-span-a")
	appendUser(t, p, sess, "会话A的部署记录")

	other := newLCMTestSession("fts-like-span-b")
	if err := p.Bootstrap(context.Background(), other); err != nil {
		t.Fatalf("bootstrap other: %v", err)
	}
	appendUser(t, p, other, "会话B的部署记录")

	stranger := newLCMTestSession("fts-like-span-x")
	stranger.UserID = testOtherUserID
	if err := p.Bootstrap(context.Background(), stranger); err != nil {
		t.Fatalf("bootstrap stranger: %v", err)
	}
	appendUser(t, p, stranger, "陌生人的部署记录")

	otherAgent := newLCMTestSession("fts-like-span-y")
	otherAgent.AgentID = "other"
	if err := p.Bootstrap(context.Background(), otherAgent); err != nil {
		t.Fatalf("bootstrap other agent: %v", err)
	}
	appendUser(t, p, otherAgent, "另一个agent的部署记录")

	// CJK BM25 search must span both same-user+agent sessions while keeping
	// another user's and another agent's content out.
	results := runSearch(t, p, sess, memory.SearchQuery{Text: "部署"})
	if len(results) != 2 {
		t.Fatalf("expected hits from both same-user sessions, got %d: %+v", len(results), results)
	}
	for _, r := range results {
		if strings.Contains(r.Content, "陌生人") || strings.Contains(r.Content, "另一个agent") {
			t.Errorf("hit leaked across user/agent boundary: %q", r.Content)
		}
	}
}

func TestSearch_SummariesSpanSessionsWithContentTime(t *testing.T) {
	db, p, sess := newSearchTestEnv(t, "fts-sum-span-a")
	convA := conversationID(t, db, sess.ID)
	// latest_at is the real end of the summarized window; OccurredAt must report
	// it (not created_at, which is when the summary row was written).
	const latestAt = "2030-01-02 03:04:05"
	insertSummaryAt(t, db, convA, "sum-span-a", "narwhal migration plan finalized", latestAt)

	other := newLCMTestSession("fts-sum-span-b")
	if err := p.Bootstrap(context.Background(), other); err != nil {
		t.Fatalf("bootstrap other: %v", err)
	}
	convB := conversationID(t, db, other.ID)
	insertSummary(t, db, convB, "sum-span-b", "narwhal rollout notes from another session")

	stranger := newLCMTestSession("fts-sum-span-x")
	stranger.UserID = testOtherUserID
	if err := p.Bootstrap(context.Background(), stranger); err != nil {
		t.Fatalf("bootstrap stranger: %v", err)
	}
	convX := conversationID(t, db, stranger.ID)
	insertSummary(t, db, convX, "sum-span-x", "narwhal secret of a different user")

	results := runSearch(t, p, sess, memory.SearchQuery{Text: "narwhal", Scope: memory.SearchScopeSummaries})
	if len(results) != 2 {
		t.Fatalf("expected summary hits from both same-user sessions, got %d: %+v", len(results), results)
	}
	for _, r := range results {
		if r.SessionID == "" {
			t.Errorf("summary hit missing origin session: %+v", r)
		}
		if strings.Contains(r.Content, "different user") {
			t.Errorf("summary hit leaked across user boundary: %q", r.Content)
		}
		if r.SourceID == "sum-span-a" && !r.OccurredAt.Equal(parseTestTime(t, latestAt)) {
			t.Errorf("OccurredAt = %v, want latest_at %s", r.OccurredAt, latestAt)
		}
	}
}

func TestSearch_IsCaseInsensitive(t *testing.T) {
	db, p, sess := newSearchTestEnv(t, "fts-like-case")
	convID := conversationID(t, db, sess.ID)
	appendUser(t, p, sess, "golang runtime notes")
	insertSummary(t, db, convID, "sum-like-case", "GoLang summary header")

	// jieba lowercases tokens, so an uppercase query matches the lowercase
	// "golang" message and the mixed-case "GoLang" summary alike.
	results := runSearch(t, p, sess, memory.SearchQuery{Text: "GOLANG", Scope: memory.SearchScopeBoth})
	if len(results) != 2 {
		t.Fatalf("expected 2 case-insensitive hits for %q, got %d: %+v", "GOLANG", len(results), results)
	}
}

// TestExpand_CrossSessionSummaryReturnsFullMessages locks in the payoff of
// cross-session search: a summary surfaced from another session of the same
// user+agent can be expanded back to its complete original messages, not just
// the search snippet. expand/describe are scoped by (user_id, agent_id), so the
// summary need not live in the caller's current conversation.
func TestExpand_CrossSessionSummaryReturnsFullMessages(t *testing.T) {
	db, p, sess := newSearchTestEnv(t, "expand-xsess-a")

	other := newLCMTestSession("expand-xsess-b")
	if err := p.Bootstrap(context.Background(), other); err != nil {
		t.Fatalf("bootstrap other: %v", err)
	}
	convB := conversationID(t, db, other.ID)

	// A leaf summary in conv B, linked to its full original messages.
	appendUser(t, p, other, "first detailed narwhal message", "second detailed narwhal message")
	ids := messageIDs(t, db, convB)
	if len(ids) != 2 {
		t.Fatalf("expected 2 messages in conv B, got %d", len(ids))
	}
	insertSummary(t, db, convB, "sum-xsess", "condensed narwhal discussion")
	for i, id := range ids {
		linkSummaryMessage(t, db, "sum-xsess", id, i)
	}

	explorer, ok := p.(memory.Explorer)
	if !ok {
		t.Fatal("provider does not implement Explorer")
	}
	// Caller is in session A; scope comes from context, not the conversation.
	ctx := authz.WithAgentID(authz.WithUserID(context.Background(), sess.UserID), sess.AgentID)

	res, err := explorer.Expand(ctx, "sum-xsess", 4000)
	if err != nil {
		t.Fatalf("expand cross-session summary: %v", err)
	}
	if len(res.Messages) != 2 {
		t.Fatalf("expected 2 full messages, got %d: %+v", len(res.Messages), res.Messages)
	}
	if !strings.Contains(res.Messages[0].Content, "first detailed narwhal") ||
		!strings.Contains(res.Messages[1].Content, "second detailed narwhal") {
		t.Errorf("expand returned wrong/partial content: %+v", res.Messages)
	}

	// Another user must not be able to expand it.
	strangerCtx := authz.WithAgentID(authz.WithUserID(context.Background(), testOtherUserID), sess.AgentID)
	if _, err := explorer.Expand(strangerCtx, "sum-xsess", 4000); err == nil {
		t.Error("expected cross-user expand to fail, got nil error")
	}
}

func TestExplorer_CondensedSummaryUsesCompactionRelationDirection(t *testing.T) {
	db, p, sess := newSearchTestEnv(t, "explorer-condensed")
	convID := conversationID(t, db, sess.ID)
	insertSummary(t, db, convID, "sum-child-a", "first constituent")
	insertSummary(t, db, convID, "sum-child-b", "second constituent")
	if _, err := db.Exec(context.Background(), `
		INSERT INTO ctx_summary (id, conversation_id, kind, depth, content, token_count)
		VALUES ('sum-root', $1, 'condensed', 1, 'combined summary', 10)`, convID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(context.Background(), `
		INSERT INTO ctx_summary_parent (summary_id, parent_summary_id, ordinal)
		VALUES ('sum-root', 'sum-child-a', 1), ('sum-root', 'sum-child-b', 2)`); err != nil {
		t.Fatal(err)
	}

	explorer := p.(memory.Explorer)
	ctx := authz.WithAgentID(authz.WithUserID(context.Background(), sess.UserID), sess.AgentID)
	description, err := explorer.Describe(ctx, "sum-root")
	if err != nil {
		t.Fatal(err)
	}
	if len(description.ParentIDs) != 0 || strings.Join(description.ChildIDs, ",") != "sum-child-a,sum-child-b" {
		t.Fatalf("root lineage was reversed: %#v", description)
	}
	childDescription, err := explorer.Describe(ctx, "sum-child-a")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(childDescription.ParentIDs, ",") != "sum-root" || len(childDescription.ChildIDs) != 0 {
		t.Fatalf("child lineage was reversed: %#v", childDescription)
	}
	expansion, err := explorer.Expand(ctx, "sum-root", 4_000)
	if err != nil {
		t.Fatal(err)
	}
	if len(expansion.Children) != 2 || expansion.Children[0].SummaryID != "sum-child-a" || expansion.Children[1].SummaryID != "sum-child-b" {
		t.Fatalf("condensed expansion did not return ordered constituents: %#v", expansion)
	}

	foreign := newLCMTestSession("explorer-condensed-foreign")
	if err := p.Bootstrap(context.Background(), foreign); err != nil {
		t.Fatal(err)
	}
	appendUser(t, p, foreign, "foreign source message")
	foreignConvID := conversationID(t, db, foreign.ID)
	foreignMessageIDs := messageIDs(t, db, foreignConvID)
	if len(foreignMessageIDs) != 1 {
		t.Fatalf("foreign message count = %d, want 1", len(foreignMessageIDs))
	}
	linkSummaryMessage(t, db, "sum-child-b", foreignMessageIDs[0], 1)
	if _, err := explorer.Expand(ctx, "sum-child-b", 4_000); err == nil {
		t.Fatal("Expand accepted a cross-conversation source message")
	}
	insertSummary(t, db, foreignConvID, "sum-foreign", "foreign constituent")
	if _, err := db.Exec(context.Background(), `
		INSERT INTO ctx_summary_parent (summary_id, parent_summary_id, ordinal)
		VALUES ('sum-root', 'sum-foreign', 3)`); err != nil {
		t.Fatal(err)
	}
	if _, err := explorer.Describe(ctx, "sum-root"); err == nil {
		t.Fatal("Describe accepted a cross-conversation constituent")
	}
	if _, err := explorer.Expand(ctx, "sum-root", 4_000); err == nil {
		t.Fatal("Expand accepted a cross-conversation constituent")
	}
	if _, err := db.Exec(context.Background(), `
		INSERT INTO ctx_summary_parent (summary_id, parent_summary_id, ordinal)
		VALUES ('sum-foreign', 'sum-child-a', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := explorer.Describe(ctx, "sum-child-a"); err == nil {
		t.Fatal("Describe accepted a cross-conversation container")
	}
}

// TestGetMessage_CrossSessionAndIsolation verifies the read-in-full companion to
// cross-session search: a message ID surfaced from another session of the same
// user+agent resolves to its complete content (search returns only a snippet),
// while other users/agents and unknown IDs are denied.
func TestGetMessage_CrossSessionAndIsolation(t *testing.T) {
	db, p, sess := newSearchTestEnv(t, "getmsg-a")

	other := newLCMTestSession("getmsg-b")
	if err := p.Bootstrap(context.Background(), other); err != nil {
		t.Fatalf("bootstrap other: %v", err)
	}
	convB := conversationID(t, db, other.ID)
	full := "this is the complete platypus message body that a snippet would truncate"
	appendUser(t, p, other, full)
	ids := messageIDs(t, db, convB)
	if len(ids) != 1 {
		t.Fatalf("expected 1 message in conv B, got %d", len(ids))
	}

	reader, ok := p.(memory.MessageReader)
	if !ok {
		t.Fatal("provider does not implement MessageReader")
	}
	ctx := authz.WithAgentID(authz.WithUserID(context.Background(), sess.UserID), sess.AgentID)

	got, err := reader.GetMessage(ctx, ids[0])
	if err != nil {
		t.Fatalf("get cross-session message: %v", err)
	}
	if got.Content != full {
		t.Errorf("expected full content %q, got %q", full, got.Content)
	}
	if got.SessionID != other.ID {
		t.Errorf("expected provenance session %q, got %q", other.ID, got.SessionID)
	}

	// Another user must not be able to read it.
	strangerCtx := authz.WithAgentID(authz.WithUserID(context.Background(), testOtherUserID), sess.AgentID)
	if _, err := reader.GetMessage(strangerCtx, ids[0]); err == nil {
		t.Error("expected cross-user get_message to fail, got nil error")
	}

	// Another agent must not be able to read it.
	otherAgentCtx := authz.WithAgentID(authz.WithUserID(context.Background(), sess.UserID), "other")
	if _, err := reader.GetMessage(otherAgentCtx, ids[0]); err == nil {
		t.Error("expected cross-agent get_message to fail, got nil error")
	}

	// Unknown ID fails.
	if _, err := reader.GetMessage(ctx, "no-such-message"); err == nil {
		t.Error("expected unknown message id to fail, got nil error")
	}
}
