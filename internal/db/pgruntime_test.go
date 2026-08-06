package db

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
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

// writeFakeInitdb installs a bin/initdb that behaves like the script says. It
// stands in for the real binary so the diagnostic can be exercised without a
// downloaded runtime.
func writeFakeInitdb(t *testing.T, binariesPath, script string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake executables need a POSIX shell")
	}
	path := filepath.Join(binariesPath, "bin", pgBinaryName("initdb"))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatalf("write initdb: %v", err)
	}
}

// A runtime whose bundled libraries do not resolve on this host fails with a
// bare exit status and no stderr, because embedded-postgres drops the child's
// output. The diagnostic has to put the loader's own message back in the error.
func TestPostgresRuntimeStartupDiagnosticSurfacesLoaderFailure(t *testing.T) {
	rt := postgresRuntimeInfo{BinariesPath: t.TempDir()}
	writeFakeInitdb(t, rt.BinariesPath, `echo "initdb: error while loading shared libraries: libicudata.so.76: cannot open shared object file" >&2; exit 127`)

	hint := rt.startupDiagnostic(nil)
	if !strings.Contains(hint, "libicudata.so.76") {
		t.Errorf("diagnostic lost the loader message: %q", hint)
	}
	if !strings.Contains(hint, rt.BinariesPath) {
		t.Errorf("diagnostic does not name the runtime: %q", hint)
	}
	if !strings.Contains(hint, "postgres download") {
		t.Errorf("diagnostic gives no way forward: %q", hint)
	}
}

func TestPostgresRuntimeStartupDiagnosticSilentWhenBinariesRun(t *testing.T) {
	rt := postgresRuntimeInfo{BinariesPath: t.TempDir()}
	writeFakeInitdb(t, rt.BinariesPath, `echo "initdb (PostgreSQL) 18.4"`)

	if hint := rt.startupDiagnostic(nil); hint != "" {
		t.Errorf("diagnostic = %q, want empty for a runnable runtime", hint)
	}
}

func TestPostgresRuntimeStartupDiagnosticSilentWithoutBinariesPath(t *testing.T) {
	var rt postgresRuntimeInfo
	if hint := rt.startupDiagnostic(nil); hint != "" {
		t.Errorf("diagnostic = %q, want empty when no runtime is configured", hint)
	}
}

// embedded-postgres sometimes passes the child's stderr through. Repeating it
// makes the error twice as long and no clearer, so the advice stands alone.
func TestPostgresRuntimeStartupDiagnosticDoesNotRepeatKnownCause(t *testing.T) {
	rt := postgresRuntimeInfo{BinariesPath: t.TempDir()}
	const loaderError = "initdb: error while loading shared libraries: libicudata.so.76: cannot open shared object file"
	writeFakeInitdb(t, rt.BinariesPath, `echo "`+loaderError+`" >&2; exit 127`)

	hint := rt.startupDiagnostic(errors.New("unable to init database: exit status 127\n" + loaderError))
	if strings.Contains(hint, loaderError) {
		t.Errorf("diagnostic repeats what the error already says: %q", hint)
	}
	if !strings.Contains(hint, "postgres download") {
		t.Errorf("diagnostic gives no way forward: %q", hint)
	}
}

// A probe must not outlive the failure it explains: the binary it runs is one
// this host has already shown it cannot be trusted to run.
func TestPostgresRuntimeStartupDiagnosticGivesUpOnAHungProbe(t *testing.T) {
	rt := postgresRuntimeInfo{BinariesPath: t.TempDir()}
	writeFakeInitdb(t, rt.BinariesPath, `sleep 60`)
	restore := startupDiagnosticTimeout
	startupDiagnosticTimeout = 200 * time.Millisecond
	t.Cleanup(func() { startupDiagnosticTimeout = restore })

	done := make(chan string, 1)
	go func() { done <- rt.startupDiagnostic(nil) }()
	select {
	case hint := <-done:
		if !strings.Contains(hint, "cannot run on this host") {
			t.Errorf("diagnostic = %q, want the runtime named", hint)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("startupDiagnostic did not return after its timeout elapsed")
	}
}

// The probe's output and the failed start's output overlap only in part: the
// loader message is shared, the command line around it is not.
func TestPostgresRuntimeStartupDiagnosticKeepsLinesTheCauseOmits(t *testing.T) {
	rt := postgresRuntimeInfo{BinariesPath: t.TempDir()}
	const shared = "initdb: error while loading shared libraries: libicudata.so.76: cannot open shared object file"
	writeFakeInitdb(t, rt.BinariesPath, `echo "`+shared+`" >&2; echo "initdb: hint: check your installation" >&2; exit 127`)

	hint := rt.startupDiagnostic(errors.New("unable to init database: exit status 127\n" + shared))
	if strings.Contains(hint, shared) {
		t.Errorf("diagnostic repeats the line the error already carries: %q", hint)
	}
	if !strings.Contains(hint, "check your installation") {
		t.Errorf("diagnostic dropped a line the error does not carry: %q", hint)
	}
}

// The diagnostic is only worth anything if a real failed start reaches it.
func TestStartEmbeddedExplainsARuntimeThatCannotRun(t *testing.T) {
	root := t.TempDir()
	binaries := filepath.Join(root, "postgres")
	writeFakeInitdb(t, binaries, `echo "initdb: error while loading shared libraries: libicudata.so.76: cannot open shared object file" >&2; exit 127`)
	touch(t, filepath.Join(binaries, "bin", pgCtlName()))
	t.Setenv(postgresRuntimeEnvName, root)

	_, err := StartEmbedded("", 0)
	if err == nil {
		t.Fatal("StartEmbedded succeeded with a runtime that cannot run")
	}
	if !strings.Contains(err.Error(), "postgres download --force") {
		t.Errorf("StartEmbedded error carries no remediation: %v", err)
	}
	if got := strings.Count(err.Error(), "libicudata.so.76"); got != 1 {
		t.Errorf("loader message appears %d times, want exactly 1: %v", got, err)
	}
}
