package manifest

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestGenerateMiseTOMLSimpleForm(t *testing.T) {
	got, err := renderBinaryTOML(ManifestBinary{
		Name:    "mytool",
		Tool:    "github:owner/repo",
		Version: "1.0.0",
	})
	if err != nil {
		t.Fatalf("generateMiseTOML: %v", err)
	}
	want := `'github:owner/repo' = '1.0.0'`
	if !strings.Contains(got, want) {
		t.Fatalf("expected simple form %q in:\n%s", want, got)
	}
}

func TestGenerateMiseTOMLRegistryTool(t *testing.T) {
	got, err := renderBinaryTOML(ManifestBinary{
		Name: "uv",
		Tool: "uv",
	})
	if err != nil {
		t.Fatalf("generateMiseTOML: %v", err)
	}
	want := `uv = 'latest'`
	if !strings.Contains(got, want) {
		t.Fatalf("expected registry tool form %q in:\n%s", want, got)
	}
}

func TestGenerateMiseTOMLTableFormDefaultsVersion(t *testing.T) {
	got, err := renderBinaryTOML(ManifestBinary{
		Name:    "gh",
		Tool:    "github:cli/cli",
		Options: map[string]any{"bin_path": "bin"},
	})
	if err != nil {
		t.Fatalf("generateMiseTOML: %v", err)
	}
	for _, want := range []string{
		"[tools.'github:cli/cli']",
		`version = 'latest'`,
		`bin_path = 'bin'`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("mise.toml missing %q:\n%s", want, got)
		}
	}
}

func TestGenerateMiseTOMLAssetPatternAloneTriggersTableForm(t *testing.T) {
	got, err := renderBinaryTOML(ManifestBinary{
		Name:    "gh",
		Tool:    "github:cli/cli",
		Version: "2.40.1",
		Options: map[string]any{"asset_pattern": "gh_*_linux_x64.tar.gz"},
	})
	if err != nil {
		t.Fatalf("generateMiseTOML: %v", err)
	}
	for _, want := range []string{
		"[tools.'github:cli/cli']",
		`version = '2.40.1'`,
		`asset_pattern = 'gh_*_linux_x64.tar.gz'`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("mise.toml missing %q:\n%s", want, got)
		}
	}
}

func TestGenerateMiseTOMLAdvancedOptions(t *testing.T) {
	got, err := renderBinaryTOML(ManifestBinary{
		Name:    "pandoc",
		Tool:    "github:jgm/pandoc",
		Version: "3.1.0",
		Options: map[string]any{
			"asset_pattern":    "pandoc-*-linux-amd64.tar.gz",
			"version_prefix":   "release-",
			"strip_components": 1,
			"bin_path":         "bin",
			"rename_exe":       "pandoc",
			"no_app":           true,
			"filter_bins":      "pandoc",
			"checksum":         "sha256:abc123",
			"prerelease":       true,
			"api_url":          "https://github.example.com/api/v3",
		},
	})
	if err != nil {
		t.Fatalf("generateMiseTOML: %v", err)
	}
	for _, want := range []string{
		"[tools.'github:jgm/pandoc']",
		`version = '3.1.0'`,
		`asset_pattern = 'pandoc-*-linux-amd64.tar.gz'`,
		`version_prefix = 'release-'`,
		`strip_components = 1`,
		`bin_path = 'bin'`,
		`rename_exe = 'pandoc'`,
		`no_app = true`,
		`filter_bins = 'pandoc'`,
		`checksum = 'sha256:abc123'`,
		`prerelease = true`,
		`api_url = 'https://github.example.com/api/v3'`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("mise.toml missing %q:\n%s", want, got)
		}
	}
}

func TestGenerateMiseTOMLBinField(t *testing.T) {
	got, err := renderBinaryTOML(ManifestBinary{
		Name:    "docker-compose",
		Tool:    "github:docker/compose",
		Version: "2.29.1",
		Options: map[string]any{"bin": "docker-compose"},
	})
	if err != nil {
		t.Fatalf("generateMiseTOML: %v", err)
	}
	for _, want := range []string{
		"[tools.'github:docker/compose']",
		`bin = 'docker-compose'`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("mise.toml missing %q:\n%s", want, got)
		}
	}
}

func TestGenerateMiseTOMLHTTPBackend(t *testing.T) {
	got, err := renderBinaryTOML(ManifestBinary{
		Name:    "sentinel",
		Tool:    "http:sentinel",
		Version: "0.26.3",
		Options: map[string]any{"url": "https://releases.hashicorp.com/sentinel/{{version}}/sentinel_{{version}}_linux_amd64.zip"},
	})
	if err != nil {
		t.Fatalf("generateMiseTOML: %v", err)
	}
	for _, want := range []string{
		"[tools.'http:sentinel']",
		`version = '0.26.3'`,
		`url = 'https://releases.hashicorp.com`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("mise.toml missing %q:\n%s", want, got)
		}
	}
}

func TestGenerateMiseTOMLHTTPWithFormat(t *testing.T) {
	got, err := renderBinaryTOML(ManifestBinary{
		Name:    "mytool",
		Tool:    "http:mytool",
		Version: "1.2.0",
		Options: map[string]any{
			"url":    "https://example.com/mytool-{{version}}-linux-amd64.tar.gz",
			"format": "tar.gz",
		},
	})
	if err != nil {
		t.Fatalf("generateMiseTOML: %v", err)
	}
	for _, want := range []string{
		"[tools.'http:mytool']",
		`format = 'tar.gz'`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("mise.toml missing %q:\n%s", want, got)
		}
	}
}

func TestGenerateMiseTOMLPipxSimple(t *testing.T) {
	got, err := renderBinaryTOML(ManifestBinary{
		Name:    "mypy",
		Tool:    "pipx:mypy",
		Version: "1.8.0",
	})
	if err != nil {
		t.Fatalf("generateMiseTOML: %v", err)
	}
	want := `'pipx:mypy' = '1.8.0'`
	if !strings.Contains(got, want) {
		t.Fatalf("expected simple form %q in:\n%s", want, got)
	}
}

func TestGenerateMiseTOMLPipxWithExtras(t *testing.T) {
	got, err := renderBinaryTOML(ManifestBinary{
		Name:    "pylint",
		Tool:    "pipx:pylint",
		Options: map[string]any{"extras": "spelling"},
	})
	if err != nil {
		t.Fatalf("generateMiseTOML: %v", err)
	}
	for _, want := range []string{
		"[tools.'pipx:pylint']",
		`extras = 'spelling'`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("mise.toml missing %q:\n%s", want, got)
		}
	}
}

func TestGenerateMiseTOMLNPM(t *testing.T) {
	got, err := renderBinaryTOML(ManifestBinary{
		Name:    "serve",
		Tool:    "npm:serve",
		Version: "14.2.0",
	})
	if err != nil {
		t.Fatalf("generateMiseTOML: %v", err)
	}
	want := `'npm:serve' = '14.2.0'`
	if !strings.Contains(got, want) {
		t.Fatalf("expected simple form %q in:\n%s", want, got)
	}
}

func TestGenerateMiseTOMLNoToolErrors(t *testing.T) {
	_, err := renderBinaryTOML(ManifestBinary{Name: "x"})
	if err == nil {
		t.Fatal("expected error for binary with no tool")
	}
}

func TestGenerateMiseTOMLLeavesToolKeyToMise(t *testing.T) {
	got, err := renderBinaryTOML(ManifestBinary{Name: "x", Tool: "github:repo"})
	if err != nil {
		t.Fatalf("generateMiseTOML: %v", err)
	}
	want := `'github:repo' = 'latest'`
	if !strings.Contains(got, want) {
		t.Fatalf("expected mise tool key form %q in:\n%s", want, got)
	}
}

// renderBinaryTOML renders a single manifest binary the way the installer does.
func renderBinaryTOML(b ManifestBinary) (string, error) {
	return renderMiseTOML([]miseTool{miseToolFromBinary(b)})
}

func TestInstallScopeIsolatesHostEnvAndPersistsConfig(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake mise script uses POSIX shell")
	}

	stellaHome := t.TempDir()
	binDir := filepath.Join(stellaHome, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("create bin dir: %v", err)
	}

	logPath := filepath.Join(stellaHome, "mise.log")
	fakeMise := filepath.Join(binDir, "mise")
	fake := `#!/bin/sh
set -eu
{
  printf 'args:%s\n' "$*"
  printf 'MISE_DATA_DIR=%s\n' "${MISE_DATA_DIR-}"
  printf 'MISE_CONFIG_DIR=%s\n' "${MISE_CONFIG_DIR-}"
  printf 'MISE_GLOBAL_CONFIG_FILE=%s\n' "${MISE_GLOBAL_CONFIG_FILE-}"
  printf 'MISE_PROJECT_ROOT=%s\n' "${MISE_PROJECT_ROOT-}"
  printf 'HOME=%s\n' "${HOME-}"
  printf 'XDG_CONFIG_HOME=%s\n' "${XDG_CONFIG_HOME-}"
} >> ` + shellQuote(logPath) + `
case "$1" in
  trust|install|reshim)
    exit 0
    ;;
  *)
    echo "unexpected command: $*" >&2
    exit 9
    ;;
esac
`
	if err := os.WriteFile(fakeMise, []byte(fake), 0o755); err != nil {
		t.Fatalf("write fake mise: %v", err)
	}

	t.Setenv("MISE_PROJECT_ROOT", "/danger/project")
	t.Setenv("MISE_CONFIG_DIR", "/danger/config")
	t.Setenv("MISE_DATA_DIR", "/danger/data")
	t.Setenv("XDG_CONFIG_HOME", "/danger/xdg")
	t.Setenv("HOME", "/danger/home")

	err := installScope(context.Background(), stellaHome, builtinScope, []miseTool{{
		Key:     "github:owner/repo",
		Version: "1.2.3",
		Lookup:  "mytool",
	}})
	if err != nil {
		t.Fatalf("installScope: %v", err)
	}

	// Config is persisted (not a temp dir) so runtime can point at it.
	configPath := ScopeConfigPath(stellaHome, builtinScope)
	cfg, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read persisted config: %v", err)
	}
	if !strings.Contains(string(cfg), `'github:owner/repo' = '1.2.3'`) {
		t.Fatalf("persisted config missing tool entry:\n%s", cfg)
	}

	// Nothing is copied into $STELLA_HOME/bin (shims-only).
	if _, err := os.Stat(filepath.Join(binDir, "mytool")); !os.IsNotExist(err) {
		t.Fatalf("expected no copied binary, stat err = %v", err)
	}

	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read fake mise log: %v", err)
	}
	log := string(logData)
	for _, want := range []string{"args:trust ", "args:install\n", "args:reshim\n"} {
		if !strings.Contains(log, want) {
			t.Fatalf("missing %q in mise log:\n%s", want, log)
		}
	}
	if strings.Contains(log, "/danger") {
		t.Fatalf("host mise env leaked into installer; log:\n%s", log)
	}
	wantData := "MISE_DATA_DIR=" + filepath.Join(stellaHome, ".mise-tools")
	if !strings.Contains(log, wantData) {
		t.Fatalf("isolated data dir not used, want %q in log:\n%s", wantData, log)
	}
	if !strings.Contains(log, "MISE_GLOBAL_CONFIG_FILE="+configPath) {
		t.Fatalf("scope config file not pointed at, want %q in log:\n%s", configPath, log)
	}
	if !strings.Contains(log, "MISE_PROJECT_ROOT=\n") {
		t.Fatalf("MISE_PROJECT_ROOT should be stripped; log:\n%s", log)
	}
}

func TestRelinkShims(t *testing.T) {
	stellaHome := t.TempDir()
	binDir := filepath.Join(stellaHome, "bin")
	shimsDir := filepath.Join(stellaHome, ".mise-tools", "shims")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(shimsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Place mise in $STELLA_HOME/bin/ (relinkShims requires it there).
	localMise := filepath.Join(binDir, runtimeBinaryName("mise"))
	if err := os.WriteFile(localMise, []byte("#!/bin/sh\necho mise"), 0o755); err != nil {
		t.Fatal(err)
	}

	hostMise := filepath.Join(t.TempDir(), "mise")
	if err := os.WriteFile(hostMise, []byte("#!/bin/sh\necho mise"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Create shim symlinks pointing to the host mise (absolute path).
	for _, name := range []string{"bun", "fd", "lark-cli"} {
		if err := os.Symlink(hostMise, filepath.Join(shimsDir, name)); err != nil {
			t.Fatal(err)
		}
	}
	// Also create a non-symlink file that should be left alone.
	if err := os.WriteFile(filepath.Join(shimsDir, "regular"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := relinkShims(stellaHome, hostMise); err != nil {
		t.Fatalf("relinkShims: %v", err)
	}

	// Verify shims now use relative paths.
	wantTarget := filepath.Join("..", "..", "bin", runtimeBinaryName("mise"))
	for _, name := range []string{"bun", "fd", "lark-cli"} {
		target, err := os.Readlink(filepath.Join(shimsDir, name))
		if err != nil {
			t.Fatalf("Readlink(%s): %v", name, err)
		}
		if target != wantTarget {
			t.Errorf("shim %s -> %q, want %q", name, target, wantTarget)
		}
	}

	// Verify regular file was not touched.
	data, err := os.ReadFile(filepath.Join(shimsDir, "regular"))
	if err != nil || string(data) != "x" {
		t.Errorf("regular file was modified")
	}

	// Verify the relative symlink actually resolves to the local binary.
	resolved, err := filepath.EvalSymlinks(filepath.Join(shimsDir, "bun"))
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	resolvedLocal, _ := filepath.EvalSymlinks(localMise)
	if resolved != resolvedLocal {
		t.Errorf("resolved shim = %q, want %q", resolved, resolvedLocal)
	}

	// Simulate another process running mise reshim after a previous successful
	// relink. Reconcile must scan again instead of trusting process-local state.
	fdShim := filepath.Join(shimsDir, "fd")
	if err := os.Remove(fdShim); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(hostMise, fdShim); err != nil {
		t.Fatal(err)
	}
	if err := relinkShims(stellaHome, hostMise); err != nil {
		t.Fatalf("relinkShims after external reshim: %v", err)
	}
	if target, err := os.Readlink(fdShim); err != nil || target != wantTarget {
		t.Fatalf("shim fd -> %q, %v; want %q", target, err, wantTarget)
	}
}

func TestRelinkShims_skipsWithoutLocalMise(t *testing.T) {
	stellaHome := t.TempDir()
	shimsDir := filepath.Join(stellaHome, ".mise-tools", "shims")
	if err := os.MkdirAll(shimsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	hostMise := filepath.Join(t.TempDir(), "mise")
	if err := os.WriteFile(hostMise, []byte("#!/bin/sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(hostMise, filepath.Join(shimsDir, "tool")); err != nil {
		t.Fatal(err)
	}

	// No $STELLA_HOME/bin/mise — relinkShims should be a no-op.
	if err := relinkShims(stellaHome, hostMise); err != nil {
		t.Fatalf("relinkShims: %v", err)
	}

	// Shim should still point to hostMise (not relinked).
	target, err := os.Readlink(filepath.Join(shimsDir, "tool"))
	if err != nil {
		t.Fatal(err)
	}
	if target != hostMise {
		t.Errorf("shim was relinked to %q, expected unchanged %q", target, hostMise)
	}

	// And mise should NOT have been copied into bin/.
	if _, err := os.Stat(filepath.Join(stellaHome, "bin", "mise")); !os.IsNotExist(err) {
		t.Error("mise should not have been copied into $STELLA_HOME/bin/")
	}
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
