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
	_, p, sess := newSearchTestEnv(t, "fts-rank")
	appendUser(t, p, sess,
		"alpha beaver alpha beaver strongdoc",
		"alpha weakdoc and unrelated padding words here",
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

func TestSearch_ConversationIsolation(t *testing.T) {
	_, p, sess := newSearchTestEnv(t, "fts-iso-a")
	appendUser(t, p, sess, "shared quokka keyword in conversation A")

	other := newLCMTestSession("fts-iso-b")
	if err := p.Bootstrap(context.Background(), other); err != nil {
		t.Fatalf("bootstrap other: %v", err)
	}
	appendUser(t, p, other, "shared quokka keyword in conversation B")

	results := runSearch(t, p, sess, memory.SearchQuery{Text: "quokka"})
	if len(results) != 1 {
		t.Fatalf("expected 1 hit scoped to conversation A, got %d", len(results))
	}
	if !strings.Contains(results[0].Content, "conversation A") {
		t.Errorf("hit leaked from another conversation: %q", results[0].Content)
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
