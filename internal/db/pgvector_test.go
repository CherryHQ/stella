package db

import (
	"context"
	"testing"

	pgvector "github.com/pgvector/pgvector-go"
)

// TestVectorCodecRoundTrip proves the pgvector codec registered in AfterConnect
// actually binds and scans a `vector` column end to end on a pooled connection.
// The vector OID is dynamic (assigned by CREATE EXTENSION) and pgx dispatches by
// registered OID, not by the database/sql Scanner/Valuer pgvector.Vector also
// implements, so without the registration this insert/scan fails. Everything runs
// on one acquired connection because the table is TEMP (connection-scoped).
func TestVectorCodecRoundTrip(t *testing.T) {
	pool := newTestDB(t)
	ctx := context.Background()

	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "CREATE TEMP TABLE vec_rt (v vector(3))"); err != nil {
		t.Fatalf("create temp table: %v", err)
	}
	want := pgvector.NewVector([]float32{1, 2, 3})
	if _, err := conn.Exec(ctx, "INSERT INTO vec_rt (v) VALUES ($1)", want); err != nil {
		t.Fatalf("insert vector: %v", err)
	}
	var got pgvector.Vector
	if err := conn.QueryRow(ctx, "SELECT v FROM vec_rt").Scan(&got); err != nil {
		t.Fatalf("scan vector: %v", err)
	}
	if g := got.Slice(); len(g) != 3 || g[0] != 1 || g[1] != 2 || g[2] != 3 {
		t.Fatalf("round trip mismatch: got %v, want [1 2 3]", g)
	}
}
