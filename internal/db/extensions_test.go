package db

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestEnsureExtensionRequirementsCreatesExtension(t *testing.T) {
	e := startExtensionTestPostgres(t)
	pool := openExtensionTestDB(t, e.DSN())
	defer pool.Close()

	ctx := context.Background()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer conn.Release()

	// vector ships in the runtime bundle's extension overlay (no preload needed),
	// so it exercises the create path against a real, always-present extension.
	reqs := []ExtensionRequirement{{Name: "vector", Required: true}}
	if err := ensureExtensionRequirements(ctx, conn, reqs); err != nil {
		t.Fatalf("ensureExtensionRequirements: %v", err)
	}

	var exists bool
	if err := conn.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'vector')").Scan(&exists); err != nil {
		t.Fatalf("query pg_extension: %v", err)
	}
	if !exists {
		t.Fatal("vector was not created")
	}
}

func TestCheckExtensionAvailableReportsMissingExtension(t *testing.T) {
	e := startExtensionTestPostgres(t)
	pool := openExtensionTestDB(t, e.DSN())
	defer pool.Close()

	ctx := context.Background()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer conn.Release()

	err = checkExtensionAvailable(ctx, conn, ExtensionRequirement{Name: "definitely_missing_stella_ext", Required: true})
	if err == nil {
		t.Fatal("checkExtensionAvailable succeeded, want error")
	}
	if !strings.Contains(err.Error(), `"definitely_missing_stella_ext" is not available`) {
		t.Fatalf("missing extension error = %q", err)
	}
}

func TestCheckExtensionPreloadedReportsMissingLibrary(t *testing.T) {
	e := startExtensionTestPostgres(t)
	pool := openExtensionTestDB(t, e.DSN())
	defer pool.Close()

	ctx := context.Background()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer conn.Release()

	// A library that is not in shared_preload_libraries surfaces an actionable
	// error naming the library and the restart it needs.
	err = checkExtensionPreloaded(ctx, conn, "definitely_not_preloaded")
	if err == nil {
		t.Fatal("checkExtensionPreloaded succeeded, want error")
	}
	for _, want := range []string{"shared_preload_libraries='definitely_not_preloaded'", "restart PostgreSQL"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("preload error %q does not contain %q", err, want)
		}
	}

	// The embedded runtime bundle preloads pg_search, so the real check passes.
	if err := checkExtensionPreloaded(ctx, conn, "pg_search"); err != nil {
		t.Fatalf("pg_search should be preloaded by the bundle: %v", err)
	}
}

func TestOptionalExtensionRequirementIsSkipped(t *testing.T) {
	e := startExtensionTestPostgres(t)
	pool := openExtensionTestDB(t, e.DSN())
	defer pool.Close()

	ctx := context.Background()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer conn.Release()

	reqs := []ExtensionRequirement{{Name: "definitely_missing_stella_ext"}}
	if err := ensureExtensionRequirements(ctx, conn, reqs); err != nil {
		t.Fatalf("ensureExtensionRequirements optional missing ext: %v", err)
	}
}

func TestPreloadContains(t *testing.T) {
	tests := []struct {
		value string
		name  string
		want  bool
	}{
		{value: "pg_search", name: "pg_search", want: true},
		{value: "pg_stat_statements, pg_search", name: "pg_search", want: true},
		{value: "pg_stat_statements,pg_search", name: "pg_search", want: true},
		{value: "pg_search_extra", name: "pg_search", want: false},
		{value: "", name: "pg_search", want: false},
	}
	for _, tt := range tests {
		if got := preloadContains(tt.value, tt.name); got != tt.want {
			t.Fatalf("preloadContains(%q, %q) = %v, want %v", tt.value, tt.name, got, tt.want)
		}
	}
}

func startExtensionTestPostgres(t *testing.T) *Embedded {
	t.Helper()
	e, err := StartEmbedded("", 0)
	if err != nil {
		t.Fatalf("StartEmbedded: %v", err)
	}
	t.Cleanup(func() { _ = e.Stop() })
	return e
}

func openExtensionTestDB(t *testing.T, dsn string) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	return pool
}
