package db

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestPostgresRuntimeFromBundleWithNestedPostgresRoot(t *testing.T) {
	root := t.TempDir()
	pgRoot := filepath.Join(root, "postgres")
	touch(t, filepath.Join(pgRoot, "bin", pgCtlName()))

	bundle, err := postgresRuntimeFromBundle(root)
	if err != nil {
		t.Fatalf("postgresRuntimeFromBundle: %v", err)
	}
	if bundle.BinariesPath != pgRoot {
		t.Fatalf("BinariesPath = %q, want %q", bundle.BinariesPath, pgRoot)
	}
}

func TestPostgresRuntimeFromBundleWithDirectPostgresRoot(t *testing.T) {
	root := t.TempDir()
	touch(t, filepath.Join(root, "bin", pgCtlName()))

	bundle, err := postgresRuntimeFromBundle(root)
	if err != nil {
		t.Fatalf("postgresRuntimeFromBundle: %v", err)
	}
	if bundle.BinariesPath != root {
		t.Fatalf("BinariesPath = %q, want %q", bundle.BinariesPath, root)
	}
}

func TestPostgresRuntimeFromBundleRejectsInvalidRoot(t *testing.T) {
	_, err := postgresRuntimeFromBundle(t.TempDir())
	if err == nil {
		t.Fatal("postgresRuntimeFromBundle succeeded, want error")
	}
	msg := err.Error()
	for _, want := range []string{postgresRuntimeEnvName, "postgres/bin/" + pgCtlName(), "bin/" + pgCtlName()} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error %q does not contain %q", msg, want)
		}
	}
}

func TestPostgresRuntimeFromBundleFindsExtensionRoots(t *testing.T) {
	root := t.TempDir()
	touch(t, filepath.Join(root, "postgres", "bin", pgCtlName()))
	if err := os.MkdirAll(filepath.Join(root, "extensions", "share", "extension"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "extensions", "lib"), 0o755); err != nil {
		t.Fatal(err)
	}

	bundle, err := postgresRuntimeFromBundle(root)
	if err != nil {
		t.Fatalf("postgresRuntimeFromBundle: %v", err)
	}
	if bundle.ExtShareRoot != filepath.Join(root, "extensions", "share") {
		t.Fatalf("ExtShareRoot = %q", bundle.ExtShareRoot)
	}
	if bundle.ExtLibRoot != filepath.Join(root, "extensions", "lib") {
		t.Fatalf("ExtLibRoot = %q", bundle.ExtLibRoot)
	}
}

func TestPostgresRuntimeFromBundleFindsPostgresAppRoots(t *testing.T) {
	root := t.TempDir()
	pgRoot := filepath.Join(root, "postgres")
	touch(t, filepath.Join(pgRoot, "bin", pgCtlName()))
	if err := os.MkdirAll(filepath.Join(pgRoot, "share", "postgresql", "extension"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(pgRoot, "lib", "postgresql"), 0o755); err != nil {
		t.Fatal(err)
	}

	bundle, err := postgresRuntimeFromBundle(root)
	if err != nil {
		t.Fatalf("postgresRuntimeFromBundle: %v", err)
	}
	if bundle.PgShareRoot != filepath.Join(pgRoot, "share", "postgresql") {
		t.Fatalf("PgShareRoot = %q", bundle.PgShareRoot)
	}
	if bundle.PgLibRoot != filepath.Join(pgRoot, "lib", "postgresql") {
		t.Fatalf("PgLibRoot = %q", bundle.PgLibRoot)
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
