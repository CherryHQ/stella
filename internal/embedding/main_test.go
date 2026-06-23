package embedding_test

import (
	"testing"

	"github.com/CherryHQ/stella/internal/db/dbtest"
)

// TestMain boots one embedded PostgreSQL per binary for the DB-backed indexer
// test. Pure-unit tests (chain, storage) live in package embedding and pay no
// startup cost: dbtest starts the server lazily on the first dbtest.New call.
func TestMain(m *testing.M) { dbtest.Main(m) }
