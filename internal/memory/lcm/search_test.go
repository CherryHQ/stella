package lcm_test

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/lcm"
	"github.com/CherryHQ/stella/pkg/ai"
)

// newSearchTestEnv builds a provider on a raw db handle so tests can both use
// the public Append path and reach behind it (insert summaries, delete rows).
func newSearchTestEnv(t *testing.T, suffix string) (*sql.DB, memory.Provider, memory.Session) {
	t.Helper()
	db := newLCMTestDB(t)
	t.Cleanup(func() { _ = db.Close() })

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

func conversationID(t *testing.T, db *sql.DB, sessionID string) string {
	t.Helper()
	var id string
	if err := db.QueryRow(`SELECT id FROM ctx_conversation WHERE session_id = ?`, sessionID).Scan(&id); err != nil {
		t.Fatalf("conversation id: %v", err)
	}
	return id
}

func insertSummary(t *testing.T, db *sql.DB, convID, id, content string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO ctx_summary (id, conversation_id, kind, depth, content, token_count)
		VALUES (?, ?, 'leaf', 0, ?, 10)`, id, convID, content)
	if err != nil {
		t.Fatalf("insert summary: %v", err)
	}
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
	// Contents kept short: the snippet window is 32 tokens, which under the
	// trigram tokenizer is only ~34 characters.
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
	// FTS snippet highlights matched terms.
	if !strings.Contains(results[0].Content, "<<alpha>>") {
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
	db, p, sess := newSearchTestEnv(t, "fts-both")
	convID := conversationID(t, db, sess.ID)
	appendUser(t, p, sess, "delta mentioned once among many other unrelated padding words in this message")
	insertSummary(t, db, convID, "sum-fts-3", "delta delta delta focused summary")

	results := runSearch(t, p, sess, memory.SearchQuery{Text: "delta", Scope: memory.SearchScopeBoth})
	if len(results) != 2 {
		t.Fatalf("expected 2 hits across sources, got %d", len(results))
	}
	if results[0].SourceType != "summary" {
		t.Errorf("expected strong summary hit ranked above weak message hit, got %+v", results)
	}
	if results[1].SourceType != "message" {
		t.Errorf("expected message hit second, got %+v", results[1])
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
	if _, err := db.Exec(`DELETE FROM ctx_item WHERE conversation_id = ?`, convID); err != nil {
		t.Fatalf("delete items: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM ctx_message WHERE conversation_id = ?`, convID); err != nil {
		t.Fatalf("delete messages: %v", err)
	}

	if got := runSearch(t, p, sess, memory.SearchQuery{Text: "xylophone"}); len(got) != 0 {
		t.Fatalf("expected 0 hits after delete, got %d: %+v", len(got), got)
	}
}

func TestSearch_SpansSessionsOfSameUserAgent(t *testing.T) {
	// Marker fits the ~34-char trigram snippet window. Both sessions share the
	// same (user_id, agent_id), so memory recall must span both — searching from
	// session A still surfaces what was said in session B.
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
	otherUser.UserID = "2"
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

func TestSearch_CJKSubstringMatch(t *testing.T) {
	_, p, sess := newSearchTestEnv(t, "fts-cjk")
	appendUser(t, p, sess, "今天讨论了部署方案的细节和时间表")

	// 3+ rune CJK query goes through trigram MATCH with snippet highlights.
	results := runSearch(t, p, sess, memory.SearchQuery{Text: "部署方案", Scope: memory.SearchScopeMessages})
	if len(results) != 1 {
		t.Fatalf("expected 1 hit for 部署方案, got %d", len(results))
	}
	if !strings.Contains(results[0].Content, "<<") {
		t.Errorf("expected snippet highlighting in MATCH path, got %q", results[0].Content)
	}
}

func TestSearch_ShortQueryFallsBackToLike(t *testing.T) {
	db, p, sess := newSearchTestEnv(t, "fts-short")
	convID := conversationID(t, db, sess.ID)
	appendUser(t, p, sess, "今天讨论了部署方案的细节")
	insertSummary(t, db, convID, "sum-fts-short", "总结：部署流程已经确定")

	// 2-char CJK queries match nothing via trigram MATCH; the LIKE fallback
	// must still find them, in both scopes, with Score 0 (no BM25 here).
	results := runSearch(t, p, sess, memory.SearchQuery{Text: "部署", Scope: memory.SearchScopeBoth})
	if len(results) != 2 {
		t.Fatalf("expected 2 fallback hits for 部署, got %d: %+v", len(results), results)
	}
	for _, r := range results {
		if r.Score != 0 {
			t.Errorf("fallback hit should carry Score 0, got %v", r.Score)
		}
	}

	msgs := runSearch(t, p, sess, memory.SearchQuery{Text: "部署", Scope: memory.SearchScopeMessages})
	if len(msgs) != 1 || msgs[0].SourceType != "message" {
		t.Errorf("messages scope fallback: expected 1 message hit, got %+v", msgs)
	}
}

func TestSearch_LikeFallbackSpansSessionsButIsolatesUsers(t *testing.T) {
	_, p, sess := newSearchTestEnv(t, "fts-like-span-a")
	appendUser(t, p, sess, "会话A的部署记录")

	other := newLCMTestSession("fts-like-span-b")
	if err := p.Bootstrap(context.Background(), other); err != nil {
		t.Fatalf("bootstrap other: %v", err)
	}
	appendUser(t, p, other, "会话B的部署记录")

	stranger := newLCMTestSession("fts-like-span-x")
	stranger.UserID = "2"
	if err := p.Bootstrap(context.Background(), stranger); err != nil {
		t.Fatalf("bootstrap stranger: %v", err)
	}
	appendUser(t, p, stranger, "陌生人的部署记录")

	// The LIKE fallback path must also span both same-user sessions while
	// keeping another user's content out.
	results := runSearch(t, p, sess, memory.SearchQuery{Text: "部署"})
	if len(results) != 2 {
		t.Fatalf("expected hits from both same-user sessions, got %d: %+v", len(results), results)
	}
	for _, r := range results {
		if strings.Contains(r.Content, "陌生人") {
			t.Errorf("fallback hit leaked across user boundary: %q", r.Content)
		}
	}
}

func TestSearch_LikeFallbackEscapesWildcards(t *testing.T) {
	_, p, sess := newSearchTestEnv(t, "fts-like-esc")
	appendUser(t, p, sess,
		"discount is 50% off",
		"discount is 500 off",
		"see a_b here",
		"see axb here",
	)

	// Both queries tokenize below the trigram minimum, so they hit the LIKE
	// fallback; % and _ must match literally, not as wildcards.
	if got := runSearch(t, p, sess, memory.SearchQuery{Text: "50%"}); len(got) != 1 {
		t.Errorf("query 50%%: expected 1 literal hit, got %d: %+v", len(got), got)
	}
	if got := runSearch(t, p, sess, memory.SearchQuery{Text: "_b"}); len(got) != 1 {
		t.Errorf("query _b: expected 1 literal hit, got %d: %+v", len(got), got)
	}
}
