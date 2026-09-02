package db

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/CherryHQ/stella/internal/db/pgruntime"
)

const (
	postgresRuntimeID = pgruntime.RuntimeVersion

	// postgresRuntimeEnvName is an internal escape hatch for testing a Stella-built
	// PostgreSQL runtime before release artifacts exist. The directory may either
	// be the PostgreSQL root itself (bin/pg_ctl) or a runtime root containing:
	//
	//   postgres/bin/pg_ctl
	//   extensions/share/extension/*.control
	//   extensions/lib/*.{so,dylib,dll}
	//
	// Keep this out of user docs until the runtime builder and license decision are
	// settled; external DSNs remain the supported advanced-search path for now.
	postgresRuntimeEnvName = "STELLA_POSTGRES_RUNTIME"
)

// Long enough for a cold binary on a slow disk, short enough that a hung probe
// cannot outlive the error it is explaining. A variable so tests can shorten it.
var startupDiagnosticTimeout = 10 * time.Second

type postgresRuntimeInfo struct {
	BinariesPath string
	RuntimePath  string
	DataPath     string
	ExtShareRoot string
	ExtLibRoot   string
	PgShareRoot  string
	PgLibRoot    string
}

func newPostgresRuntimeInfo(dataDir, tmpDir string) (postgresRuntimeInfo, error) {
	var rt postgresRuntimeInfo

	if dataDir != "" {
		rt.DataPath = dataDir
		// Keep persistent data and disposable runtime extraction separate:
		// embedded-postgres removes RuntimePath on every start, while DataPath must
		// survive restarts. BinariesPath, when configured, points at the immutable
		// runtime root and is not extracted into this directory.
		rt.RuntimePath = filepath.Join(filepath.Dir(dataDir), "pg-runtime", postgresRuntimeCacheName(), "runtime")
	} else {
		rt.DataPath = filepath.Join(tmpDir, "data")
		rt.RuntimePath = filepath.Join(tmpDir, "runtime")
	}

	runtimeRoot := os.Getenv(postgresRuntimeEnvName)
	if runtimeRoot == "" {
		if root, ok := downloadedPostgresRuntimeRoot(dataDir); ok {
			runtimeRoot = root
		} else {
			// Embedded PostgreSQL only carries pgvector and pg_search when a Stella
			// runtime is installed. Refuse instead of booting vanilla PostgreSQL and
			// failing later with missing extension errors.
			return postgresRuntimeInfo{}, fmt.Errorf(
				"db: no PostgreSQL runtime for %s/%s (expected %s). Download it with `stellad postgres download`, set STELLA_DATABASE_URL to an external PostgreSQL with pg_search and pgvector, or set %s to an extracted runtime. %s",
				runtime.GOOS, runtime.GOARCH, postgresRuntimeID, postgresRuntimeEnvName, pgruntime.MissingRuntimeHint())
		}
	}

	install, err := postgresRuntimeFromRoot(runtimeRoot)
	if err != nil {
		return postgresRuntimeInfo{}, err
	}
	rt.BinariesPath = install.BinariesPath
	rt.ExtShareRoot = install.ExtShareRoot
	rt.ExtLibRoot = install.ExtLibRoot
	rt.PgShareRoot = install.PgShareRoot
	rt.PgLibRoot = install.PgLibRoot
	return rt, nil
}

func postgresRuntimeCacheName() string {
	return postgresRuntimeID + "-" + runtime.GOOS + "-" + runtime.GOARCH
}

func downloadedPostgresRuntimeRoot(dataDir string) (string, bool) {
	source, ok := pgruntime.DefaultRuntimeSource()
	if !ok {
		return "", false
	}
	var home string
	switch detected, ok := stellaHomeForRuntime(); {
	case dataDir != "":
		home = filepath.Dir(dataDir)
	case ok:
		home = detected
	default:
		return "", false
	}
	root := pgruntime.RuntimeRoot(home, source)
	if _, err := postgresRuntimeFromRoot(root); err != nil {
		return "", false
	}
	return root, true
}

func stellaHomeForRuntime() (string, bool) {
	// Ephemeral test clusters often override STELLA_HOME per test; the runtime is
	// immutable, so keep one shared download in the user's default Stella home.
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", false
	}
	return filepath.Join(home, ".stella"), true
}

type postgresRuntimeInstall struct {
	BinariesPath string
	ExtShareRoot string
	ExtLibRoot   string
	PgShareRoot  string
	PgLibRoot    string
}

func postgresRuntimeFromRoot(root string) (postgresRuntimeInstall, error) {
	root = filepath.Clean(root)
	binariesPath, ok := locatePostgresHome(root)
	if !ok {
		return postgresRuntimeInstall{}, fmt.Errorf(
			"db: %s must point to a PostgreSQL runtime containing postgres/bin/%s, "+
				"postgres/lib/postgresql/<major>/bin/%s, or bin/%s",
			postgresRuntimeEnvName, pgCtlName(), pgCtlName(), pgCtlName())
	}

	install := postgresRuntimeInstall{
		BinariesPath: binariesPath,
		PgShareRoot:  postgresShareRoot(binariesPath),
		PgLibRoot:    postgresLibraryRoot(binariesPath),
	}
	if dirExists(filepath.Join(root, "extensions", "share", "extension")) {
		install.ExtShareRoot = filepath.Join(root, "extensions", "share")
	}
	if dirExists(filepath.Join(root, "extensions", "lib")) {
		install.ExtLibRoot = filepath.Join(root, "extensions", "lib")
	}
	return install, nil
}

func (rt postgresRuntimeInfo) startParameters() map[string]string {
	params := map[string]string{}
	pathSep := string(os.PathListSeparator)
	// Put the Unix socket in the cluster's own data dir. PGDG builds compile
	// unix_socket_directories=/var/run/postgresql, which a non-root embedded server
	// cannot create its socket/lock file in; the data dir is always writable and
	// per-instance, which also isolates the socket between parallel test clusters.
	if rt.DataPath != "" {
		params["unix_socket_directories"] = rt.DataPath
	}
	if rt.ExtShareRoot != "" {
		controlPath := rt.ExtShareRoot
		if rt.PgShareRoot != "" {
			controlPath += pathSep + rt.PgShareRoot
		}
		params["extension_control_path"] = controlPath + pathSep + "$system"
	}
	if rt.ExtLibRoot != "" {
		dynamicPath := rt.ExtLibRoot
		if rt.PgLibRoot != "" {
			dynamicPath += pathSep + rt.PgLibRoot
		}
		params["dynamic_library_path"] = dynamicPath
	}
	// pg_search must be preloaded before CREATE EXTENSION can load it. Look in the
	// downloaded extension lib dir and PostgreSQL's own lib dir (a plain runtime
	// root where pg_search sits in $libdir) — either is valid. A broken or
	// ABI-incompatible library is intentionally fatal at PostgreSQL start.
	for _, libRoot := range []string{rt.ExtLibRoot, rt.PgLibRoot} {
		if libRoot != "" && fileExists(filepath.Join(libRoot, postgresSharedLibraryName("pg_search"))) {
			params["shared_preload_libraries"] = "pg_search"
			break
		}
	}
	return params
}

// locatePostgresHome finds the directory holding bin/<pg_ctl> inside a downloaded
// or external runtime root. It accepts three layouts:
//
//   - flat runtime:        <root>/postgres/bin                          (darwin postgresapp, single prefix)
//   - /usr-mirror runtime: <root>/postgres/lib/postgresql/<major>/bin   (PGDG linux, split prefix)
//   - plain runtime root: <root>/bin                                   (STELLA_POSTGRES_RUNTIME → a PG install)
//
// The split-prefix Linux runtime mirrors /usr so the backend can relocate its
// share dir (timezonesets, bki) from its own executable path; its bin therefore
// lives under lib/postgresql/<major>, not directly under postgres/.
func locatePostgresHome(root string) (string, bool) {
	candidates := []string{
		filepath.Join(root, "postgres"),
		root,
	}
	if nested, err := filepath.Glob(filepath.Join(root, "postgres", "lib", "postgresql", "*")); err == nil {
		candidates = append(candidates, nested...)
	}
	for _, home := range candidates {
		if fileExists(filepath.Join(home, "bin", pgCtlName())) {
			return home, true
		}
	}
	return "", false
}

func postgresShareRoot(root string) string {
	if dirExists(filepath.Join(root, "share", "postgresql", "extension")) {
		return filepath.Join(root, "share", "postgresql")
	}
	if dirExists(filepath.Join(root, "share", "extension")) {
		return filepath.Join(root, "share")
	}
	return ""
}

func postgresLibraryRoot(root string) string {
	if dirExists(filepath.Join(root, "lib", "postgresql")) {
		return filepath.Join(root, "lib", "postgresql")
	}
	return filepath.Join(root, "lib")
}

func pgCtlName() string {
	return pgBinaryName("pg_ctl")
}

func pgBinaryName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

// startupDiagnostic explains a failed start when the cause is that the
// runtime's binaries cannot run at all, and returns "" otherwise. A runtime
// whose bundled libraries do not resolve on this host fails as "exit status
// 127", which says nothing about what to do; embedded-postgres passes the
// child's stderr through only sometimes. Re-running the cheapest binary in the
// bundle settles both questions, and cause is only appended when the failure
// does not already carry it.
//
// Called only after a start has already failed, so its cost never lands on a
// healthy path.
func (rt postgresRuntimeInfo) startupDiagnostic(cause error) string {
	if rt.BinariesPath == "" {
		return ""
	}
	// A diagnostic must never outlast the failure it explains. The probe runs a
	// binary this host has already shown it cannot be trusted to run, so bound
	// it and let WaitDelay cut a child that holds the output pipe open.
	ctx, cancel := context.WithTimeout(context.Background(), startupDiagnosticTimeout)
	defer cancel()
	initdb := filepath.Join(rt.BinariesPath, "bin", pgBinaryName("initdb"))
	cmd := exec.CommandContext(ctx, initdb, "--version")
	cmd.WaitDelay = time.Second
	out, err := cmd.CombinedOutput()
	if err == nil {
		return ""
	}
	detail := strings.TrimSpace(string(out))
	if detail == "" {
		detail = err.Error()
	}
	detail = withoutLinesFrom(detail, cause)
	if detail != "" {
		detail = ": " + detail
	}
	return fmt.Sprintf(
		"the PostgreSQL runtime at %s cannot run on this host%s. "+
			"Its bundled libraries may not resolve here — install the matching system libraries "+
			"(on Debian/Ubuntu usually libicu) or re-download the runtime with "+
			"`stellad postgres download --force`",
		rt.BinariesPath, detail)
}

// withoutLinesFrom drops the lines of detail that cause already reports. It
// works per line because the probe's output and the failed start's output
// overlap partially: the loader message is the same, the command line around it
// is not.
func withoutLinesFrom(detail string, cause error) string {
	if cause == nil {
		return detail
	}
	reported := cause.Error()
	var kept []string
	for line := range strings.SplitSeq(detail, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" && !strings.Contains(reported, trimmed) {
			kept = append(kept, trimmed)
		}
	}
	return strings.Join(kept, "\n")
}

func postgresSharedLibraryName(name string) string {
	switch runtime.GOOS {
	case "darwin":
		return name + ".dylib"
	case "windows":
		return name + ".dll"
	default:
		return name + ".so"
	}
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
