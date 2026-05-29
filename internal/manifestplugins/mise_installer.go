package manifestplugins

import (
	"context"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
)

// miseToolsDir returns the MISE_DATA_DIR path for isolated mise installs.
func miseToolsDir(stellaHome string) string {
	return filepath.Join(stellaHome, ".mise-tools")
}

// findMiseBin returns the path to the mise binary. It prefers the Stella-managed
// binary in $STELLA_HOME/bin, then falls back to mise on PATH.
func findMiseBin(stellaHome string) (string, error) {
	local := filepath.Join(stellaHome, "bin", runtimeBinaryName("mise"))
	if _, err := os.Stat(local); err == nil {
		return local, nil
	}
	if path, err := exec.LookPath("mise"); err == nil {
		return path, nil
	}
	return "", fmt.Errorf("mise not found at %s or on PATH", local)
}

func bootstrapMise(_ context.Context, stellaHome string) error {
	_, err := findMiseBin(stellaHome)
	return err
}

var misePassthroughEnv = []string{
	"PATH",
	"SystemRoot",
	"WINDIR",
	"ComSpec",
	"PATHEXT",
	"TMPDIR",
	"TMP",
	"TEMP",
	"LANG",
	"LC_ALL",
	"SSL_CERT_FILE",
	"SSL_CERT_DIR",
	"HTTP_PROXY",
	"HTTPS_PROXY",
	"ALL_PROXY",
	"NO_PROXY",
	"http_proxy",
	"https_proxy",
	"all_proxy",
	"no_proxy",
	"GITHUB_TOKEN",
	"GH_TOKEN",
}

func isolatedMiseEnv(stellaHome string) ([]string, error) {
	dataDir := miseToolsDir(stellaHome)
	paths := map[string]string{
		"MISE_DATA_DIR":   dataDir,
		"MISE_CONFIG_DIR": filepath.Join(dataDir, "config"),
		"MISE_CACHE_DIR":  filepath.Join(dataDir, "cache"),
		"MISE_STATE_DIR":  filepath.Join(dataDir, "state"),
		"MISE_SHIMS_DIR":  filepath.Join(dataDir, "shims"),
		"HOME":            filepath.Join(dataDir, "home"),
		"XDG_CONFIG_HOME": filepath.Join(dataDir, "xdg", "config"),
		"XDG_CACHE_HOME":  filepath.Join(dataDir, "xdg", "cache"),
		"XDG_STATE_HOME":  filepath.Join(dataDir, "xdg", "state"),
	}
	if runtime.GOOS == "windows" {
		paths["USERPROFILE"] = paths["HOME"]
	}

	for _, dir := range paths {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create isolated mise dir %s: %w", dir, err)
		}
	}

	env := make(map[string]string, len(misePassthroughEnv)+len(paths)+2)
	for _, key := range misePassthroughEnv {
		if value, ok := os.LookupEnv(key); ok {
			env[key] = value
		}
	}
	maps.Copy(env, paths)
	env["MISE_YES"] = "1"
	env["MISE_NO_ANALYTICS"] = "1"
	env["MISE_EXPERIMENTAL"] = "1"

	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, key+"="+env[key])
	}
	return out, nil
}

func runtimeBinaryName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

// stringOption returns a non-empty string tool option value.
func stringOption(options map[string]any, key string) (string, bool) {
	value, ok := options[key]
	if !ok {
		return "", false
	}
	s, ok := value.(string)
	return s, ok && s != ""
}
