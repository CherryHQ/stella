package db

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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
		return rt, nil
	}

	bundle, err := postgresRuntimeFromBundle(bundleRoot)
	if err != nil {
		return postgresRuntimeInfo{}, err
	}
	rt.BinariesPath = bundle.BinariesPath
	rt.ExtShareRoot = bundle.ExtShareRoot
	rt.ExtLibRoot = bundle.ExtLibRoot
	return rt, nil
}

func postgresRuntimeCacheName() string {
	return postgresBundleID + "-" + runtime.GOOS + "-" + runtime.GOARCH
}

type postgresRuntimeBundle struct {
	BinariesPath string
	ExtShareRoot string
	ExtLibRoot   string
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

	bundle := postgresRuntimeBundle{BinariesPath: binariesPath}
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
		params["extension_control_path"] = rt.ExtShareRoot + pathSep + "$system"
	}
	if rt.ExtLibRoot != "" {
		params["dynamic_library_path"] = rt.ExtLibRoot + pathSep + "$libdir"
		if fileExists(filepath.Join(rt.ExtLibRoot, postgresSharedLibraryName("pg_search"))) {
			// pg_search must be preloaded before CREATE EXTENSION can work. A broken
			// or ABI-incompatible library is intentionally fatal at PostgreSQL start,
			// which keeps bad internal test bundles from degrading silently.
			params["shared_preload_libraries"] = "pg_search"
		}
	}
	return params
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
