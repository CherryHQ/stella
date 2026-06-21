package db

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/CherryHQ/stella/resources/pgbundle"
)

const (
	postgresBundleID = "pg18.3.0-search0.24.1-vector0.8.3"

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
			return rt, nil
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
	binariesPath := root
	if fileExists(filepath.Join(root, "postgres", "bin", pgCtlName())) {
		binariesPath = filepath.Join(root, "postgres")
	}
	if !fileExists(filepath.Join(binariesPath, "bin", pgCtlName())) {
		return postgresRuntimeBundle{}, fmt.Errorf("db: %s must point to a PostgreSQL runtime bundle containing postgres/bin/%s or bin/%s", postgresRuntimeEnvName, pgCtlName(), pgCtlName())
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
		if fileExists(filepath.Join(rt.ExtLibRoot, postgresSharedLibraryName("pg_search"))) {
			// pg_search must be preloaded before CREATE EXTENSION can work. A broken
			// or ABI-incompatible library is intentionally fatal at PostgreSQL start,
			// which keeps bad internal test bundles from degrading silently.
			params["shared_preload_libraries"] = "pg_search"
		}
	}
	return params
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
