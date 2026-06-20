package db

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Package db's own tests cannot use internal/db/dbtest — that helper imports this
// package — so this mirrors it locally: one embedded server per test binary, a
// template migrated once, and a fresh cloned database per test via
// CREATE DATABASE ... TEMPLATE.
var (
	pkgTestOnce   sync.Once
	pkgTestServer *Embedded
	pkgTestAdmin  *pgxpool.Pool
	pkgTestErr    error
	pkgTestSeq    atomic.Int64
)

const pkgTestTemplate = "stella_tmpl"

func pkgTestEnsure() {
	pkgTestOnce.Do(func() {
		pkgTestServer, pkgTestErr = StartEmbedded("", 0)
		if pkgTestErr != nil {
			return
		}
		pkgTestAdmin, pkgTestErr = pgxpool.New(context.Background(), pkgTestServer.DSNFor("postgres"))
		if pkgTestErr != nil {
			return
		}
		if _, err := pkgTestAdmin.Exec(context.Background(), "CREATE DATABASE "+pkgTestTemplate); err != nil {
			pkgTestErr = fmt.Errorf("create template: %w", err)
			return
		}
		tmpl, err := OpenDB(pkgTestServer.DSNFor(pkgTestTemplate))
		if err != nil {
			pkgTestErr = fmt.Errorf("migrate template: %w", err)
			return
		}
		tmpl.Close()
	})
}

// newTestDB returns a fresh, fully-migrated, isolated database for one test,
// dropped when the test ends.
func newTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pkgTestEnsure()
	if pkgTestErr != nil {
		t.Fatalf("newTestDB: %v", pkgTestErr)
	}
	name := fmt.Sprintf("dbtest_%d", pkgTestSeq.Add(1))
	if _, err := pkgTestAdmin.Exec(context.Background(), fmt.Sprintf("CREATE DATABASE %s TEMPLATE %s", name, pkgTestTemplate)); err != nil {
		t.Fatalf("newTestDB: create %s: %v", name, err)
	}
	db, err := OpenDB(pkgTestServer.DSNFor(name))
	if err != nil {
		t.Fatalf("newTestDB: open %s: %v", name, err)
	}
	t.Cleanup(func() {
		db.Close()
		_, _ = pkgTestAdmin.Exec(context.Background(), "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1", name)
		_, _ = pkgTestAdmin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name)
	})
	return db
}

func TestMain(m *testing.M) {
	code := m.Run()
	if pkgTestServer != nil {
		_ = pkgTestServer.Stop()
	}
	os.Exit(code)
}
