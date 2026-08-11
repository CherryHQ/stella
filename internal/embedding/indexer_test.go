package embedding_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

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
