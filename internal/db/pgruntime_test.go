package db

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestPostgresRuntimeFromRootWithNestedPostgresRoot(t *testing.T) {
	root := t.TempDir()
	pgRoot := filepath.Join(root, "postgres")
	touch(t, filepath.Join(pgRoot, "bin", pgCtlName()))

	install, err := postgresRuntimeFromRoot(root)
	if err != nil {
		t.Fatalf("postgresRuntimeFromRoot: %v", err)
	}
	if install.BinariesPath != pgRoot {
		t.Fatalf("BinariesPath = %q, want %q", install.BinariesPath, pgRoot)
	}
}

func TestPostgresRuntimeFromRootWithDirectPostgresRoot(t *testing.T) {
	root := t.TempDir()
	touch(t, filepath.Join(root, "bin", pgCtlName()))

	install, err := postgresRuntimeFromRoot(root)
	if err != nil {
		t.Fatalf("postgresRuntimeFromRoot: %v", err)
	}
	if install.BinariesPath != root {
		t.Fatalf("BinariesPath = %q, want %q", install.BinariesPath, root)
	}
}

func TestPostgresRuntimeFromRootWithSplitPrefixLinuxRoot(t *testing.T) {
	root := t.TempDir()
	// PGDG linux installs mirror /usr so the backend can relocate its share dir:
	// bin lives under postgres/lib/postgresql/<major>, not directly under postgres/.
	home := filepath.Join(root, "postgres", "lib", "postgresql", "18")
	touch(t, filepath.Join(home, "bin", pgCtlName()))
	if err := os.MkdirAll(filepath.Join(home, "lib"), 0o755); err != nil {
		t.Fatal(err)
	}

	install, err := postgresRuntimeFromRoot(root)
	if err != nil {
		t.Fatalf("postgresRuntimeFromRoot: %v", err)
	}
	if install.BinariesPath != home {
		t.Fatalf("BinariesPath = %q, want %q", install.BinariesPath, home)
	}
	if install.PgLibRoot != filepath.Join(home, "lib") {
		t.Fatalf("PgLibRoot = %q, want %q", install.PgLibRoot, filepath.Join(home, "lib"))
	}
}

func TestPostgresRuntimeFromRootRejectsInvalidRoot(t *testing.T) {
	_, err := postgresRuntimeFromRoot(t.TempDir())
	if err == nil {
		t.Fatal("postgresRuntimeFromRoot succeeded, want error")
	}
	msg := err.Error()
	for _, want := range []string{postgresRuntimeEnvName, "postgres/bin/" + pgCtlName(), "bin/" + pgCtlName()} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error %q does not contain %q", msg, want)
		}
	}
}

func TestPostgresRuntimeFromRootFindsExtensionRoots(t *testing.T) {
	root := t.TempDir()
	touch(t, filepath.Join(root, "postgres", "bin", pgCtlName()))
	if err := os.MkdirAll(filepath.Join(root, "extensions", "share", "extension"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "extensions", "lib"), 0o755); err != nil {
		t.Fatal(err)
	}

	install, err := postgresRuntimeFromRoot(root)
	if err != nil {
		t.Fatalf("postgresRuntimeFromRoot: %v", err)
	}
	if install.ExtShareRoot != filepath.Join(root, "extensions", "share") {
		t.Fatalf("ExtShareRoot = %q", install.ExtShareRoot)
	}
	if install.ExtLibRoot != filepath.Join(root, "extensions", "lib") {
		t.Fatalf("ExtLibRoot = %q", install.ExtLibRoot)
	}
}

func TestPostgresRuntimeFromRootFindsPostgresAppRoots(t *testing.T) {
	root := t.TempDir()
	pgRoot := filepath.Join(root, "postgres")
	touch(t, filepath.Join(pgRoot, "bin", pgCtlName()))
	if err := os.MkdirAll(filepath.Join(pgRoot, "share", "postgresql", "extension"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(pgRoot, "lib", "postgresql"), 0o755); err != nil {
		t.Fatal(err)
	}

	install, err := postgresRuntimeFromRoot(root)
	if err != nil {
		t.Fatalf("postgresRuntimeFromRoot: %v", err)
	}
	if install.PgShareRoot != filepath.Join(pgRoot, "share", "postgresql") {
		t.Fatalf("PgShareRoot = %q", install.PgShareRoot)
	}
	if install.PgLibRoot != filepath.Join(pgRoot, "lib", "postgresql") {
		t.Fatalf("PgLibRoot = %q", install.PgLibRoot)
	}
}

func TestPostgresRuntimeStartParameters(t *testing.T) {
	root := t.TempDir()
	extShare := filepath.Join(root, "extensions", "share")
	extLib := filepath.Join(root, "extensions", "lib")
	if err := os.MkdirAll(extLib, 0o755); err != nil {
		t.Fatal(err)
	}
	touch(t, filepath.Join(extLib, postgresSharedLibraryName("pg_search")))

	pgShare := filepath.Join(root, "postgres", "share", "postgresql")
	pgLib := filepath.Join(root, "postgres", "lib", "postgresql")
	rt := postgresRuntimeInfo{ExtShareRoot: extShare, ExtLibRoot: extLib, PgShareRoot: pgShare, PgLibRoot: pgLib}
	got := rt.startParameters()
	sep := string(os.PathListSeparator)
	want := map[string]string{
		"extension_control_path":   extShare + sep + pgShare + sep + "$system",
		"dynamic_library_path":     extLib + sep + pgLib,
		"shared_preload_libraries": "pg_search",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("startParameters = %#v, want %#v", got, want)
	}
}

func TestPostgresRuntimeStartParametersPreloadsFromPgLib(t *testing.T) {
	// A plain runtime root (STELLA_POSTGRES_RUNTIME) has no external extensions
	// dir, so pg_search sits in PostgreSQL's own lib dir; preload must still fire.
	pgLib := t.TempDir()
	touch(t, filepath.Join(pgLib, postgresSharedLibraryName("pg_search")))
	rt := postgresRuntimeInfo{PgLibRoot: pgLib}
	got := rt.startParameters()
	if got["shared_preload_libraries"] != "pg_search" {
		t.Fatalf("expected pg_search preload from PgLibRoot, got %#v", got)
	}
}

func TestPostgresRuntimeStartParametersDoNotPreloadMissingPGSearch(t *testing.T) {
	rt := postgresRuntimeInfo{ExtLibRoot: t.TempDir()}
	got := rt.startParameters()
	if _, ok := got["shared_preload_libraries"]; ok {
		t.Fatalf("shared_preload_libraries set without pg_search library: %#v", got)
	}
}

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o755); err != nil {
		t.Fatal(err)
	}
}
