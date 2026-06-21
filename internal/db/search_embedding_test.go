package db

import (
	"bytes"
	"context"
	"testing"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestSearchEmbeddingLifecycle(t *testing.T) {
	db := newTestDB(t)
	defer db.Close()
	q := sqlc.New(db)
	ctx := context.Background()

	created, err := q.UpsertSearchEmbedding(ctx, sqlc.UpsertSearchEmbeddingParams{
		OwnerKind:   "ctx_message",
		OwnerID:     "019b4023-6d96-7bc5-b18f-2e59b814fa4d",
		Model:       "text-embedding-3-small",
		Dims:        3,
		ContentHash: []byte{1, 2, 3},
		Embedding:   []byte{0, 0, 128, 63},
	})
	if err != nil {
		t.Fatalf("UpsertSearchEmbedding create: %v", err)
	}
	if created.ID == "" {
		t.Fatal("created embedding has empty id")
	}

	updated, err := q.UpsertSearchEmbedding(ctx, sqlc.UpsertSearchEmbeddingParams{
		OwnerKind:   created.OwnerKind,
		OwnerID:     created.OwnerID,
		Model:       created.Model,
		Dims:        4,
		ContentHash: []byte{4, 5, 6},
		Embedding:   []byte{0, 0, 0, 64},
	})
	if err != nil {
		t.Fatalf("UpsertSearchEmbedding update: %v", err)
	}
	if updated.ID != created.ID {
		t.Fatalf("upsert changed id: got %q want %q", updated.ID, created.ID)
	}
	if updated.Dims != 4 || !bytes.Equal(updated.ContentHash, []byte{4, 5, 6}) || !bytes.Equal(updated.Embedding, []byte{0, 0, 0, 64}) {
		t.Fatalf("updated embedding = %+v", updated)
	}
	if updated.UpdatedAt.Before(created.UpdatedAt) {
		t.Fatalf("updated_at moved backwards: %s before %s", updated.UpdatedAt, created.UpdatedAt)
	}

	got, err := q.GetSearchEmbedding(ctx, sqlc.GetSearchEmbeddingParams{
		OwnerKind: created.OwnerKind,
		OwnerID:   created.OwnerID,
		Model:     created.Model,
	})
	if err != nil {
		t.Fatalf("GetSearchEmbedding: %v", err)
	}
	if got.ID != created.ID {
		t.Fatalf("GetSearchEmbedding id = %q, want %q", got.ID, created.ID)
	}

	byOwner, err := q.ListSearchEmbeddingByOwner(ctx, sqlc.ListSearchEmbeddingByOwnerParams{
		OwnerKind: created.OwnerKind,
		OwnerID:   created.OwnerID,
	})
	if err != nil {
		t.Fatalf("ListSearchEmbeddingByOwner: %v", err)
	}
	if len(byOwner) != 1 || byOwner[0].ID != created.ID {
		t.Fatalf("ListSearchEmbeddingByOwner = %+v", byOwner)
	}

	byModel, err := q.ListSearchEmbeddingByModel(ctx, sqlc.ListSearchEmbeddingByModelParams{
		Model: created.Model,
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("ListSearchEmbeddingByModel: %v", err)
	}
	if len(byModel) != 1 || byModel[0].ID != created.ID {
		t.Fatalf("ListSearchEmbeddingByModel = %+v", byModel)
	}

	if err := q.DeleteSearchEmbedding(ctx, sqlc.DeleteSearchEmbeddingParams{
		OwnerKind: created.OwnerKind,
		OwnerID:   created.OwnerID,
		Model:     created.Model,
	}); err != nil {
		t.Fatalf("DeleteSearchEmbedding: %v", err)
	}
	byOwner, err = q.ListSearchEmbeddingByOwner(ctx, sqlc.ListSearchEmbeddingByOwnerParams{
		OwnerKind: created.OwnerKind,
		OwnerID:   created.OwnerID,
	})
	if err != nil {
		t.Fatalf("ListSearchEmbeddingByOwner after delete: %v", err)
	}
	if len(byOwner) != 0 {
		t.Fatalf("ListSearchEmbeddingByOwner after delete = %+v", byOwner)
	}
}
