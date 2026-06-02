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

	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

// miseToolsDir returns the MISE_DATA_DIR path for isolated mise installs. The
// layout is owned by pkg/sandbox so the install side and the sandbox PATH stay
// in lockstep.
func miseToolsDir(stellaHome string) string {
	return pkgsandbox.MiseToolsDir(stellaHome)
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
	// Directories the install needs on disk; the shared base (miseBaseEnv)
	// already covers DATA/CONFIG/CACHE/STATE, install adds shims + an isolated
	// HOME/XDG so nothing leaks into the host user's profile.
	installDirs := map[string]string{
		"MISE_SHIMS_DIR":  filepath.Join(dataDir, "shims"),
		"HOME":            filepath.Join(dataDir, "home"),
		"XDG_CONFIG_HOME": filepath.Join(dataDir, "xdg", "config"),
		"XDG_CACHE_HOME":  filepath.Join(dataDir, "xdg", "cache"),
		"XDG_STATE_HOME":  filepath.Join(dataDir, "xdg", "state"),
	}
	if runtime.GOOS == "windows" {
		installDirs["USERPROFILE"] = installDirs["HOME"]
	}

	base := miseBaseEnv(stellaHome)
	for _, dir := range []string{
		base["MISE_DATA_DIR"], base["MISE_CONFIG_DIR"], base["MISE_CACHE_DIR"], base["MISE_STATE_DIR"],
		installDirs["MISE_SHIMS_DIR"], installDirs["HOME"],
		installDirs["XDG_CONFIG_HOME"], installDirs["XDG_CACHE_HOME"], installDirs["XDG_STATE_HOME"],
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create isolated mise dir %s: %w", dir, err)
		}
	}

	env := make(map[string]string, len(misePassthroughEnv)+len(base)+len(installDirs))
	for _, key := range misePassthroughEnv {
		if value, ok := os.LookupEnv(key); ok {
			env[key] = value
		}
	}
	maps.Copy(env, base)
	maps.Copy(env, installDirs)

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

// relinkShims rewrites mise shim symlinks to use relative paths so they
// resolve correctly inside bwrap sandboxes where STELLA_HOME is remapped.
//
// mise reshim creates symlinks pointing to the absolute host path of the mise
// binary (e.g. /home/user/.local/bin/mise). Inside bwrap, that path doesn't
// exist. By rewriting shims to relative symlinks (../../bin/mise), they work
// on both the host and inside the sandbox — the relative traversal from
// .mise-tools/shims/ to bin/ resolves identically regardless of the mount root.
func relinkShims(stellaHome, _ string) error {
	// Only relink if $STELLA_HOME/bin/mise exists. We never copy an
	// arbitrary PATH-resolved mise into a trusted location.
	localBin := filepath.Join(stellaHome, "bin", runtimeBinaryName("mise"))
	if _, err := os.Stat(localBin); err != nil {
		return nil //nolint:nilerr // no local mise binary → nothing to relink
	}

	shimsDir := filepath.Join(miseToolsDir(stellaHome), "shims")
	entries, err := os.ReadDir(shimsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read shims dir: %w", err)
	}

	relTarget := filepath.Join("..", "..", "bin", runtimeBinaryName("mise"))

	for _, e := range entries {
		if e.Type()&os.ModeSymlink == 0 {
			continue
		}
		shimPath := filepath.Join(shimsDir, e.Name())
		target, err := os.Readlink(shimPath)
		if err != nil || target == relTarget {
			continue
		}
		// Atomic replace: create temp symlink then rename over the old one.
		tmp := shimPath + ".tmp"
		_ = os.Remove(tmp)
		if err := os.Symlink(relTarget, tmp); err != nil {
			return fmt.Errorf("relink shim %s: %w", e.Name(), err)
		}
		if err := os.Rename(tmp, shimPath); err != nil {
			_ = os.Remove(tmp)
			return fmt.Errorf("relink shim %s: %w", e.Name(), err)
		}
	}
	return nil
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
