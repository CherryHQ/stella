package db

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/CherryHQ/stella/resources/pgbundle"
)

const (
	postgresBundleID = "pg18.4-pgvector0.8.2-pgsearch0.24.1"

	// postgresRuntimeEnvName is an internal escape hatch for testing a Stella-built
	// PostgreSQL runtime before release artifacts exist. The directory may either
	// be the PostgreSQL root itself (bin/pg_ctl) or a bundle root containing:
	//
	//   postgres/bin/pg_ctl
	//   extensions/share/extension/*.control
	//   extensions/lib/*.{so,dylib,dll}
	//
	// Keep this out of user docs until the bundle builder and license decision are
	// settled; external DSNs remain the supported advanced-search path for now.
	postgresRuntimeEnvName = "STELLA_POSTGRES_RUNTIME"
)

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
		// bundle root and is not extracted into this directory.
		rt.RuntimePath = filepath.Join(filepath.Dir(dataDir), "pg-runtime", postgresRuntimeCacheName(), "runtime")
	} else {
		rt.DataPath = filepath.Join(tmpDir, "data")
		rt.RuntimePath = filepath.Join(tmpDir, "runtime")
	}

	bundleRoot := os.Getenv(postgresRuntimeEnvName)
	if bundleRoot == "" {
		if root, ok, err := pgbundle.EnsureBundle(postgresBundleCacheRoot(dataDir, tmpDir)); err != nil {
			return postgresRuntimeInfo{}, err
		} else if !ok {
			// No bundle for this build/platform. Embedded PostgreSQL only carries
			// pgvector and pg_search when a runtime bundle is present; booting vanilla
			// embedded-postgres would silently drop them, and search now hard-requires
			// both. Refuse instead of degrading: a supported platform must embed the
			// bundle, anything else must point at an external PostgreSQL that already
			// has the extensions.
			return postgresRuntimeInfo{}, fmt.Errorf(
				"db: no embedded PostgreSQL runtime bundle for %s/%s (expected %s). "+
					"Run scripts/fetch-pg-runtime.sh on a supported platform to embed it, "+
					"or set STELLA_DATABASE_URL to an external PostgreSQL with pg_search and pgvector installed",
				runtime.GOOS, runtime.GOARCH, postgresBundleID)
		} else {
			bundleRoot = root
		}
	}

	bundle, err := postgresRuntimeFromBundle(bundleRoot)
	if err != nil {
		return postgresRuntimeInfo{}, err
	}
	rt.BinariesPath = bundle.BinariesPath
	rt.ExtShareRoot = bundle.ExtShareRoot
	rt.ExtLibRoot = bundle.ExtLibRoot
	rt.PgShareRoot = bundle.PgShareRoot
	rt.PgLibRoot = bundle.PgLibRoot
	return rt, nil
}

func postgresRuntimeCacheName() string {
	return postgresBundleID + "-" + runtime.GOOS + "-" + runtime.GOARCH
}

func postgresBundleCacheRoot(dataDir, tmpDir string) string {
	if dataDir != "" {
		return filepath.Join(filepath.Dir(dataDir), "pg-runtime", postgresRuntimeCacheName(), "bundles")
	}
	return filepath.Join(tmpDir, "bundles")
}

type postgresRuntimeBundle struct {
	BinariesPath string
	ExtShareRoot string
	ExtLibRoot   string
	PgShareRoot  string
	PgLibRoot    string
}

func postgresRuntimeFromBundle(root string) (postgresRuntimeBundle, error) {
	root = filepath.Clean(root)
	binariesPath, ok := locatePostgresHome(root)
	if !ok {
		return postgresRuntimeBundle{}, fmt.Errorf(
			"db: %s must point to a PostgreSQL runtime bundle containing postgres/bin/%s, "+
				"postgres/lib/postgresql/<major>/bin/%s, or bin/%s",
			postgresRuntimeEnvName, pgCtlName(), pgCtlName(), pgCtlName())
	}

	bundle := postgresRuntimeBundle{
		BinariesPath: binariesPath,
		PgShareRoot:  postgresShareRoot(binariesPath),
		PgLibRoot:    postgresLibraryRoot(binariesPath),
	}
	if dirExists(filepath.Join(root, "extensions", "share", "extension")) {
		bundle.ExtShareRoot = filepath.Join(root, "extensions", "share")
	}
	if dirExists(filepath.Join(root, "extensions", "lib")) {
		bundle.ExtLibRoot = filepath.Join(root, "extensions", "lib")
	}
	return bundle, nil
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
	// external extension lib dir (bundle layout) and PostgreSQL's own lib dir (a
	// plain runtime root where pg_search sits in $libdir) — either is a valid place
	// to find it. A broken or ABI-incompatible library is intentionally fatal at
	// PostgreSQL start, which keeps bad internal test bundles from degrading silently.
	for _, libRoot := range []string{rt.ExtLibRoot, rt.PgLibRoot} {
		if libRoot != "" && fileExists(filepath.Join(libRoot, postgresSharedLibraryName("pg_search"))) {
			params["shared_preload_libraries"] = "pg_search"
			break
		}
	}
	return params
}

// locatePostgresHome finds the directory holding bin/<pg_ctl> inside an extracted
// bundle or external runtime root. It accepts three layouts:
//
//   - flat bundle:        <root>/postgres/bin                          (darwin postgresapp, single prefix)
//   - /usr-mirror bundle: <root>/postgres/lib/postgresql/<major>/bin   (PGDG linux, split prefix)
//   - plain runtime root: <root>/bin                                   (STELLA_POSTGRES_RUNTIME → a PG install)
//
// The split-prefix linux bundle mirrors /usr so the backend can relocate its
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
	if runtime.GOOS == "windows" {
		return "pg_ctl.exe"
	}
	return "pg_ctl"
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
