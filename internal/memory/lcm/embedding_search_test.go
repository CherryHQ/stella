package lcm_test

import (
	"context"
	"reflect"
	"testing"
	"time"

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

	const summaryID = "summary-knn"
	if _, err := db.Exec(ctx, `INSERT INTO ctx_summary (id, conversation_id, kind, depth, content, token_count) VALUES ($1,$2,'leaf',0,'summary vector content',5)`, summaryID, convID); err != nil {
		t.Fatalf("insert summary: %v", err)
	}
	if err := q.UpsertSummaryEmbedding(ctx, sqlc.UpsertSummaryEmbeddingParams{
		SummaryID: summaryID, Model: "space-a", ContentHash: []byte("h"), Embedding: vec1536(1, 0, 0),
	}); err != nil {
		t.Fatalf("upsert summary embedding: %v", err)
	}
	summaries, err := q.SearchSummaryEmbeddings(ctx, sqlc.SearchSummaryEmbeddingsParams{
		Query: vec1536(1, 0, 0), Model: "space-a",
		UserID: pgtype.Text{String: userID, Valid: true}, AgentID: pgtype.Text{String: agentID, Valid: true}, Limit: 10,
	})
	if err != nil || len(summaries) != 1 {
		t.Fatalf("active summary vector search = %#v, err=%v; want one hit", summaries, err)
	}

	if _, err := db.Exec(ctx, `UPDATE ctx_conversation SET archived = true WHERE id = $1`, convID); err != nil {
		t.Fatalf("archive conversation: %v", err)
	}
	res, err = q.SearchMessageEmbeddings(ctx, sqlc.SearchMessageEmbeddingsParams{
		Query: vec1536(1, 0, 0), Model: "space-a",
		UserID: pgtype.Text{String: userID, Valid: true}, AgentID: pgtype.Text{String: agentID, Valid: true}, Limit: 10,
	})
	if err != nil || len(res) != 0 {
		t.Fatalf("archived message vector search = %#v, err=%v; want no hits", res, err)
	}
	summaries, err = q.SearchSummaryEmbeddings(ctx, sqlc.SearchSummaryEmbeddingsParams{
		Query: vec1536(1, 0, 0), Model: "space-a",
		UserID: pgtype.Text{String: userID, Valid: true}, AgentID: pgtype.Text{String: agentID, Valid: true}, Limit: 10,
	})
	if err != nil || len(summaries) != 0 {
		t.Fatalf("archived summary vector search = %#v, err=%v; want no hits", summaries, err)
	}
}

// TestSearchEmbeddings_StableLimitAfterScope pins the exact-KNN contract: rows
// outside the active tenant scope cannot consume the candidate budget, and SQL
// resolves equal distances by content time then id before applying LIMIT.
func TestSearchEmbeddings_StableLimitAfterScope(t *testing.T) {
	db := dbtest.New(t)
	q := sqlc.New(db)
	ctx := context.Background()

	const userID, agentID, model = "user-stable-knn", "agent-stable-knn", "space-stable-knn"
	activeConversationID := uuid.NewString()
	archivedConversationID := uuid.NewString()
	foreignUserConversationID := uuid.NewString()
	foreignAgentConversationID := uuid.NewString()
	for _, fixture := range []struct {
		id, sessionID, ownerID, executorID string
		archived                           bool
	}{
		{id: activeConversationID, sessionID: "active-stable-knn", ownerID: userID, executorID: agentID},
		{id: archivedConversationID, sessionID: "archived-stable-knn", ownerID: userID, executorID: agentID, archived: true},
		{id: foreignUserConversationID, sessionID: "foreign-user-stable-knn", ownerID: "other-user", executorID: agentID},
		{id: foreignAgentConversationID, sessionID: "foreign-agent-stable-knn", ownerID: userID, executorID: "other-agent"},
	} {
		if _, err := db.Exec(ctx, `INSERT INTO ctx_conversation (id, session_id, channel, kind, agent_id, user_id, archived) VALUES ($1,$2,'test','chat',$3,$4,$5)`,
			fixture.id, fixture.sessionID, fixture.executorID, fixture.ownerID, fixture.archived); err != nil {
			t.Fatalf("insert conversation %s: %v", fixture.sessionID, err)
		}
	}

	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	messageIDs := []string{
		"00000000-0000-4000-8000-000000000001",
		"00000000-0000-4000-8000-000000000002",
		"00000000-0000-4000-8000-000000000003",
	}
	for i, id := range messageIDs {
		createdAt := base
		if i > 0 {
			createdAt = base.Add(time.Minute)
		}
		if _, err := db.Exec(ctx, `INSERT INTO ctx_message (id, conversation_id, seq, role, event_type, content, token_count, actor_type, created_at) VALUES ($1,$2,$3,'user','text',$4,5,$5,$6)`,
			id, activeConversationID, i+1, "active message", eventlog.ActorHuman, createdAt); err != nil {
			t.Fatalf("insert active message %s: %v", id, err)
		}
		if err := q.UpsertMessageEmbedding(ctx, sqlc.UpsertMessageEmbeddingParams{MessageID: id, Model: model, ContentHash: []byte("h"), Embedding: vec1536(1, 0.1)}); err != nil {
			t.Fatalf("embed active message %s: %v", id, err)
		}
	}

	summaryIDs := []string{"stable-summary-1", "stable-summary-2", "stable-summary-3"}
	for i, id := range summaryIDs {
		createdAt := base
		if i > 0 {
			createdAt = base.Add(time.Minute)
		}
		if _, err := db.Exec(ctx, `INSERT INTO ctx_summary (id, conversation_id, kind, depth, content, token_count, created_at, latest_at) VALUES ($1,$2,'leaf',0,'active summary',5,$3,$3)`,
			id, activeConversationID, createdAt); err != nil {
			t.Fatalf("insert active summary %s: %v", id, err)
		}
		if err := q.UpsertSummaryEmbedding(ctx, sqlc.UpsertSummaryEmbeddingParams{SummaryID: id, Model: model, ContentHash: []byte("h"), Embedding: vec1536(1, 0.1)}); err != nil {
			t.Fatalf("embed active summary %s: %v", id, err)
		}
	}

	// More exact forbidden matches than the requested limits prove that the
	// complete authorized tenant/archive scope is formed before distance ordering
	// and LIMIT, rather than filtered after a global nearest-neighbor walk.
	for _, decoyConversationID := range []string{archivedConversationID, foreignUserConversationID, foreignAgentConversationID} {
		for i := range 12 {
			messageID := uuid.NewString()
			if _, err := db.Exec(ctx, `INSERT INTO ctx_message (id, conversation_id, seq, role, event_type, content, token_count, actor_type) VALUES ($1,$2,$3,'user','text','forbidden decoy',5,$4)`,
				messageID, decoyConversationID, i+1, eventlog.ActorHuman); err != nil {
				t.Fatalf("insert forbidden message: %v", err)
			}
			if err := q.UpsertMessageEmbedding(ctx, sqlc.UpsertMessageEmbeddingParams{MessageID: messageID, Model: model, ContentHash: []byte("h"), Embedding: vec1536(1, 0)}); err != nil {
				t.Fatalf("embed forbidden message: %v", err)
			}
			summaryID := "forbidden-stable-summary-" + uuid.NewString()
			if _, err := db.Exec(ctx, `INSERT INTO ctx_summary (id, conversation_id, kind, depth, content, token_count) VALUES ($1,$2,'leaf',0,'forbidden decoy',5)`, summaryID, decoyConversationID); err != nil {
				t.Fatalf("insert forbidden summary: %v", err)
			}
			if err := q.UpsertSummaryEmbedding(ctx, sqlc.UpsertSummaryEmbeddingParams{SummaryID: summaryID, Model: model, ContentHash: []byte("h"), Embedding: vec1536(1, 0)}); err != nil {
				t.Fatalf("embed forbidden summary: %v", err)
			}
		}
	}

	wantMessages := []string{messageIDs[2], messageIDs[1], messageIDs[0]}
	wantSummaries := []string{summaryIDs[2], summaryIDs[1], summaryIDs[0]}
	for _, limit := range []int32{1, 2, 3} {
		messages, err := q.SearchMessageEmbeddings(ctx, sqlc.SearchMessageEmbeddingsParams{
			Query: vec1536(1, 0), Model: model, UserID: pgtype.Text{String: userID, Valid: true}, AgentID: pgtype.Text{String: agentID, Valid: true}, Limit: limit,
		})
		if err != nil {
			t.Fatalf("search messages limit %d: %v", limit, err)
		}
		gotMessages := make([]string, 0, len(messages))
		for _, row := range messages {
			gotMessages = append(gotMessages, row.ID)
		}
		if !reflect.DeepEqual(gotMessages, wantMessages[:limit]) {
			t.Fatalf("message ids limit %d = %v, want %v", limit, gotMessages, wantMessages[:limit])
		}

		summaries, err := q.SearchSummaryEmbeddings(ctx, sqlc.SearchSummaryEmbeddingsParams{
			Query: vec1536(1, 0), Model: model, UserID: pgtype.Text{String: userID, Valid: true}, AgentID: pgtype.Text{String: agentID, Valid: true}, Limit: limit,
		})
		if err != nil {
			t.Fatalf("search summaries limit %d: %v", limit, err)
		}
		gotSummaries := make([]string, 0, len(summaries))
		for _, row := range summaries {
			gotSummaries = append(gotSummaries, row.ID)
		}
		if !reflect.DeepEqual(gotSummaries, wantSummaries[:limit]) {
			t.Fatalf("summary ids limit %d = %v, want %v", limit, gotSummaries, wantSummaries[:limit])
		}
	}
}
