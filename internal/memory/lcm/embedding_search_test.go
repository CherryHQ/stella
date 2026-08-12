package lcm_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	pgvector "github.com/pgvector/pgvector-go"

	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// vec1536 builds a storage-width vector with the given leading components and
// zeros elsewhere, so tests can express directions compactly.
func vec1536(prefix ...float32) pgvector.Vector {
	v := make([]float32, 1536)
	copy(v, prefix)
	return pgvector.NewVector(v)
}

// TestSearchMessageEmbeddings_RanksByCosineAndIsolatesSpace proves the two
// guarantees the vector lane depends on: KNN orders by cosine similarity, and
// WHERE model isolates vector spaces so a vector from another model never leaks
// into the results even when it points the same direction as the query.
func TestSearchMessageEmbeddings_RanksByCosineAndIsolatesSpace(t *testing.T) {
	db := dbtest.New(t)
	q := sqlc.New(db)
	ctx := context.Background()

	convID := uuid.NewString()
	const userID, agentID = "user-knn", "agent-knn"
	if _, err := db.Exec(ctx, `INSERT INTO ctx_conversation (id, session_id, channel, kind, agent_id, user_id) VALUES ($1,$2,'test','chat',$3,$4)`,
		convID, "sess-knn", agentID, userID); err != nil {
		t.Fatalf("insert conversation: %v", err)
	}

	ids := map[string]string{}
	seq := 0
	for _, name := range []string{"A", "B", "C"} {
		id := uuid.NewString()
		ids[name] = id
		seq++
		if _, err := db.Exec(ctx, `INSERT INTO ctx_message (id, conversation_id, seq, role, event_type, content, token_count, actor_type) VALUES ($1,$2,$3,'user','text',$4,5,$5)`,
			id, convID, seq, "msg "+name, eventlog.ActorHuman); err != nil {
			t.Fatalf("insert message %s: %v", name, err)
		}
	}

	upsert := func(id, model string, v pgvector.Vector) {
		t.Helper()
		if err := q.UpsertMessageEmbedding(ctx, sqlc.UpsertMessageEmbeddingParams{
			MessageID: id, Model: model, ContentHash: []byte("h"), Embedding: v,
		}); err != nil {
			t.Fatalf("upsert %s: %v", id, err)
		}
	}
	upsert(ids["A"], "space-a", vec1536(1, 0, 0)) // identical direction to query
	upsert(ids["B"], "space-a", vec1536(0, 1, 0)) // orthogonal to query
	upsert(ids["C"], "space-b", vec1536(1, 0, 0)) // identical to A, but a DIFFERENT space

	res, err := q.SearchMessageEmbeddings(ctx, sqlc.SearchMessageEmbeddingsParams{
		Query:   vec1536(1, 0, 0),
		Model:   "space-a",
		UserID:  pgtype.Text{String: userID, Valid: true},
		AgentID: pgtype.Text{String: agentID, Valid: true},
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}

	// Space isolation: C must not appear despite being identical to A.
	if len(res) != 2 {
		t.Fatalf("expected 2 results from space-a, got %d", len(res))
	}
	if res[0].ID != ids["A"] {
		t.Fatalf("expected A (identical to query) first, got id %s", res[0].ID)
	}
	if res[0].Score < 0.99 {
		t.Fatalf("A should have ~1.0 cosine similarity, got %f", res[0].Score)
	}
	if res[1].ID != ids["B"] || res[1].Score > 0.01 {
		t.Fatalf("B should rank last with ~0 similarity, got id %s score %f", res[1].ID, res[1].Score)
	}
}
