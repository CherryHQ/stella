package lcm_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	pgvector "github.com/pgvector/pgvector-go"

	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/lcm"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// fakeQueryEmbedder always embeds a query as the same fixed direction in one
// space, so a test can pre-seed embedding rows whose cosine ranking it controls.
type fakeQueryEmbedder struct {
	vec   pgvector.Vector
	model string
}

func (f fakeQueryEmbedder) EmbedQuery(_ context.Context, _ string) (pgvector.Vector, string, error) {
	return f.vec, f.model, nil
}

// TestHybridSearch_VectorLaneSurfacesNonLexicalHit is the end-to-end proof that
// wiring a query embedder into the provider engages the semantic lane: a message
// that shares no lexical token with the query but whose embedding points at the
// query vector is invisible to pure BM25 yet surfaces once the embedder is
// configured, fused alongside the lexical hit.
func TestHybridSearch_VectorLaneSurfacesNonLexicalHit(t *testing.T) {
	db := newLCMTestDB(t)
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()

	const space = "space-test"
	emb := fakeQueryEmbedder{vec: vec1536(1, 0, 0), model: space}

	p, err := lcm.New(db, nil, nil, lcm.WithQueryEmbedder(emb))
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })

	sess := newLCMTestSession("hybrid")
	if err := p.Bootstrap(ctx, sess); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	// lexHit shares the "alpha" token (BM25 match) but its embedding is orthogonal
	// to the query; vecHit shares no token but its embedding is identical to the
	// query direction, so only the vector lane can find it.
	appendUser(t, p, sess, "alpha beaver discussion", "zzz unrelated payload words")

	q := sqlc.New(db)
	convID := conversationID(t, db, sess.ID)
	rows, err := db.Query(ctx, `SELECT id, content FROM ctx_message WHERE conversation_id = $1 ORDER BY seq`, convID)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	type msg struct{ id, content string }
	var msgs []msg
	for rows.Next() {
		var m msg
		if err := rows.Scan(&m.id, &m.content); err != nil {
			t.Fatalf("scan: %v", err)
		}
		msgs = append(msgs, m)
	}
	rows.Close()
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	for _, m := range msgs {
		v := vec1536(0, 1, 0) // orthogonal to query => near-zero cosine
		if m.content == "zzz unrelated payload words" {
			v = vec1536(1, 0, 0) // identical to query => ~1.0 cosine
		}
		if err := q.UpsertMessageEmbedding(ctx, sqlc.UpsertMessageEmbeddingParams{
			MessageID: m.id, Model: space, ContentHash: []byte("h"), Embedding: v,
		}); err != nil {
			t.Fatalf("upsert embedding: %v", err)
		}
	}

	results := runSearch(t, p, sess, memory.SearchQuery{Text: "alpha", Scope: memory.SearchScopeMessages})

	// The vector-only message must appear; pure BM25 on "alpha" would never return
	// it. Both lanes' hits are fused into the result set.
	var sawVecOnly, sawLexical bool
	for _, r := range results {
		switch {
		case strings.Contains(r.Content, "zzz unrelated"):
			sawVecOnly = true
		case strings.Contains(r.Content, "alpha"):
			sawLexical = true
		}
	}
	if !sawVecOnly {
		t.Errorf("vector lane did not surface the non-lexical hit: %+v", results)
	}
	if !sawLexical {
		t.Errorf("lexical hit missing from fused results: %+v", results)
	}

	// Control: the same query against a provider WITHOUT an embedder returns only
	// the lexical hit, confirming the vector hit came from the semantic lane.
	plain, err := lcm.New(db, nil, nil)
	if err != nil {
		t.Fatalf("new plain provider: %v", err)
	}
	t.Cleanup(func() { _ = plain.Close() })
	lexOnly := runSearch(t, plain, sess, memory.SearchQuery{Text: "alpha", Scope: memory.SearchScopeMessages})
	for _, r := range lexOnly {
		if strings.Contains(r.Content, "zzz unrelated") {
			t.Errorf("pure-BM25 provider leaked a vector-only hit: %+v", lexOnly)
		}
	}
}

func TestHybridSearch_ExactScopeFindsActiveRowsPastArchivedCandidates(t *testing.T) {
	db := newLCMTestDB(t)
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	const userID, agentID, space = "user-filtered-hnsw", "agent-filtered-hnsw", "space-filtered-hnsw"
	queryVector := vec1536(1, 0, 0)

	insertConversation := func(sessionID string, archived bool) string {
		t.Helper()
		id := uuid.NewString()
		if _, err := db.Exec(ctx, `INSERT INTO ctx_conversation (id, session_id, channel, kind, agent_id, user_id, archived) VALUES ($1,$2,'test','chat',$3,$4,$5)`, id, sessionID, agentID, userID, archived); err != nil {
			t.Fatalf("insert conversation %s: %v", sessionID, err)
		}
		return id
	}
	q := sqlc.New(db)
	insertMessage := func(conversationID string, seq int, content string, vector pgvector.Vector) {
		t.Helper()
		id := uuid.NewString()
		if _, err := db.Exec(ctx, `INSERT INTO ctx_message (id, conversation_id, seq, role, event_type, content, token_count, actor_type) VALUES ($1,$2,$3,'user','text',$4,5,$5)`, id, conversationID, seq, content, eventlog.ActorHuman); err != nil {
			t.Fatalf("insert message %d: %v", seq, err)
		}
		if err := q.UpsertMessageEmbedding(ctx, sqlc.UpsertMessageEmbeddingParams{MessageID: id, Model: space, ContentHash: []byte("h"), Embedding: vector}); err != nil {
			t.Fatalf("upsert message embedding %d: %v", seq, err)
		}
	}
	insertSummary := func(conversationID, id, content string, vector pgvector.Vector) {
		t.Helper()
		if _, err := db.Exec(ctx, `INSERT INTO ctx_summary (id, conversation_id, kind, depth, content, token_count) VALUES ($1,$2,'leaf',0,$3,5)`, id, conversationID, content); err != nil {
			t.Fatalf("insert summary %s: %v", id, err)
		}
		if err := q.UpsertSummaryEmbedding(ctx, sqlc.UpsertSummaryEmbeddingParams{SummaryID: id, Model: space, ContentHash: []byte("h"), Embedding: vector}); err != nil {
			t.Fatalf("upsert summary embedding %s: %v", id, err)
		}
	}

	archivedConversationID := insertConversation("archived-hnsw", true)
	for i := 1; i <= 60; i++ {
		// These archived rows are exact matches and exceed the default ef_search=40
		// candidate window, so a non-iterative filtered scan can stop too early.
		insertMessage(archivedConversationID, i, "archived vector decoy", queryVector)
		insertSummary(archivedConversationID, fmt.Sprintf("archived-summary-%d", i), "archived summary vector decoy", queryVector)
	}
	activeConversationID := insertConversation("active-hnsw", false)
	insertMessage(activeConversationID, 1, "active vector first", vec1536(1, 0.01, 0))
	insertMessage(activeConversationID, 2, "active vector second", vec1536(1, 0.20, 0))
	insertSummary(activeConversationID, "active-summary-first", "active summary vector first", vec1536(1, 0.01, 0))
	insertSummary(activeConversationID, "active-summary-second", "active summary vector second", vec1536(1, 0.20, 0))
	if _, err := db.Exec(ctx, "ANALYZE ctx_message_embedding, ctx_summary_embedding, ctx_message, ctx_summary, ctx_conversation"); err != nil {
		t.Fatalf("analyze vector fixtures: %v", err)
	}

	p, err := lcm.New(db, nil, nil, lcm.WithQueryEmbedder(fakeQueryEmbedder{vec: queryVector, model: space}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	for _, scope := range []memory.SearchScope{memory.SearchScopeMessages, memory.SearchScopeSummaries} {
		results := runSearch(t, p, memory.Session{ID: "active-hnsw", UserID: userID, AgentID: agentID}, memory.SearchQuery{Text: "semantic-only-query", Scope: scope, Limit: 2})
		if len(results) != 2 {
			t.Fatalf("exact scoped search %d results=%d, want 2 active rows: %+v", scope, len(results), results)
		}
		if !strings.Contains(results[0].Content, "first") || !strings.Contains(results[1].Content, "second") {
			t.Fatalf("exact scoped search %d order/content mismatch: %+v", scope, results)
		}
	}
}
