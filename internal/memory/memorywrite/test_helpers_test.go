package memorywrite_test

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// openTestDB is shared by memorywrite integration tests and intentionally has
// no dependency on the removed legacy Group Memory writer.
func openTestDB(t *testing.T) (*pgxpool.Pool, *sqlc.Queries) {
	t.Helper()
	db := dbtest.New(t)
	return db, sqlc.New(db)
}
