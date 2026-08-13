package embedding_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	pgvector "github.com/pgvector/pgvector-go"

	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/embedding"
	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// fakeEmbedder returns a fixed vector per input in a fixed space, counting calls
// so the test can assert no work happens on an already-current pass.
type fakeEmbedder struct {
	model string
	calls int
}

func (f *fakeEmbedder) Embed(_ context.Context, req embedding.Request) (embedding.Result, error) {
	f.calls++
	vs := make([][]float32, len(req.Texts))
	for i := range vs {
		vs[i] = []float32{1, 0, 0}
	}
	return embedding.Result{Vectors: vs, Model: f.model}, nil
}

func TestIndexer_BackfillPopulatesThenIsIdempotent(t *testing.T) {
	db := dbtest.New(t)
	q := sqlc.New(db)
	ctx := context.Background()

	convID := uuid.NewString()
	if _, err := db.Exec(ctx, `INSERT INTO ctx_conversation (id, session_id, channel, kind, agent_id, user_id) VALUES ($1,'sess-bf','test','chat','agent','user')`, convID); err != nil {
		t.Fatalf("insert conversation: %v", err)
	}
	for i := 1; i <= 3; i++ {
		if _, err := db.Exec(ctx, `INSERT INTO ctx_message (id, conversation_id, seq, role, event_type, content, token_count, actor_type) VALUES ($1,$2,$3,'user','text',$4,5,$5)`,
			uuid.NewString(), convID, i, "message body", eventlog.ActorHuman); err != nil {
			t.Fatalf("insert message %d: %v", i, err)
		}
	}

	emb := &fakeEmbedder{model: "space-x"}
	ix := embedding.NewIndexer(q, emb, embedding.IndexConfig{Model: "space-x", BatchSize: 100})

	n, err := ix.BackfillOnce(ctx)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if n != 3 {
		t.Fatalf("expected 3 rows embedded, got %d", n)
	}

	var count int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM ctx_message_embedding WHERE model = 'space-x'`).Scan(&count); err != nil {
		t.Fatalf("count embeddings: %v", err)
	}
	if count != 3 {
		t.Fatalf("expected 3 stored embeddings, got %d", count)
	}

	// Second pass: all rows are current in space-x, so nothing is embedded and the
	// embedder is not called again.
	callsAfterFirst := emb.calls
	n, err = ix.BackfillOnce(ctx)
	if err != nil {
		t.Fatalf("second backfill: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected 0 rows on idempotent pass, got %d", n)
	}
	if emb.calls != callsAfterFirst {
		t.Fatalf("embedder called again on idempotent pass: %d -> %d", callsAfterFirst, emb.calls)
	}
}

func TestIndexer_BackfillSkipsArchivedMessagesAndSummariesAfterModelSwitch(t *testing.T) {
	db := dbtest.New(t)
	q := sqlc.New(db)
	ctx := context.Background()

	activeConversationID := uuid.NewString()
	archivedConversationID := uuid.NewString()
	for _, fixture := range []struct {
		id, sessionID string
		archived      bool
	}{
		{id: activeConversationID, sessionID: "sess-active"},
		{id: archivedConversationID, sessionID: "sess-archived", archived: true},
	} {
		if _, err := db.Exec(ctx, `INSERT INTO ctx_conversation (id, session_id, channel, kind, agent_id, user_id, archived) VALUES ($1,$2,'test','chat','agent','user',$3)`, fixture.id, fixture.sessionID, fixture.archived); err != nil {
			t.Fatalf("insert conversation %s: %v", fixture.sessionID, err)
		}
	}

	for i, conversationID := range []string{activeConversationID, archivedConversationID} {
		messageID := uuid.NewString()
		if _, err := db.Exec(ctx, `INSERT INTO ctx_message (id, conversation_id, seq, role, event_type, content, token_count, actor_type) VALUES ($1,$2,1,'user','text',$3,5,$4)`,
			messageID, conversationID, "message body", eventlog.ActorHuman); err != nil {
			t.Fatalf("insert message %d: %v", i, err)
		}
		summaryID := fmt.Sprintf("summary-%d", i)
		if _, err := db.Exec(ctx, `INSERT INTO ctx_summary (id, conversation_id, kind, depth, content, token_count) VALUES ($1,$2,'leaf',0,'summary body',5)`, summaryID, conversationID); err != nil {
			t.Fatalf("insert summary %d: %v", i, err)
		}
		// Seed an old vector space so this specifically proves model-switch
		// backfill does not revisit archived transcript sources.
		if err := q.UpsertMessageEmbedding(ctx, sqlc.UpsertMessageEmbeddingParams{MessageID: messageID, Model: "space-old", ContentHash: []byte("old"), Embedding: pgvector.NewVector(make([]float32, 1536))}); err != nil {
			t.Fatalf("seed message embedding %d: %v", i, err)
		}
		if err := q.UpsertSummaryEmbedding(ctx, sqlc.UpsertSummaryEmbeddingParams{SummaryID: summaryID, Model: "space-old", ContentHash: []byte("old"), Embedding: pgvector.NewVector(make([]float32, 1536))}); err != nil {
			t.Fatalf("seed summary embedding %d: %v", i, err)
		}
	}

	emb := &fakeEmbedder{model: "space-new"}
	ix := embedding.NewIndexer(q, emb, embedding.IndexConfig{Model: "space-new", BatchSize: 100})
	n, err := ix.BackfillOnce(ctx)
	if err != nil {
		t.Fatalf("model-switch backfill: %v", err)
	}
	if n != 2 {
		t.Fatalf("embedded rows=%d, want active message and summary only", n)
	}

	var activeCount, archivedCount int
	if err := db.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE c.archived = false), count(*) FILTER (WHERE c.archived = true)
		FROM (
			SELECT m.conversation_id, e.model FROM ctx_message_embedding e JOIN ctx_message m ON m.id = e.message_id
			UNION ALL
			SELECT s.conversation_id, e.model FROM ctx_summary_embedding e JOIN ctx_summary s ON s.id = e.summary_id
		) embedded
		JOIN ctx_conversation c ON c.id = embedded.conversation_id
		WHERE embedded.model = 'space-new'`).Scan(&activeCount, &archivedCount); err != nil {
		t.Fatalf("count switched embeddings: %v", err)
	}
	if activeCount != 2 || archivedCount != 0 {
		t.Fatalf("new-space embeddings active=%d archived=%d, want 2/0", activeCount, archivedCount)
	}
}
