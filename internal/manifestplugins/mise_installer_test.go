package manifestplugins

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestGenerateMiseTOMLSimpleForm(t *testing.T) {
	got, err := generateMiseTOML(ManifestBinary{
		Name:    "mytool",
		Backend: "github",
		Repo:    "owner/repo",
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

func TestGenerateMiseTOMLTableFormDefaultsVersion(t *testing.T) {
	got, err := generateMiseTOML(ManifestBinary{
		Name:    "gh",
		Backend: "github",
		Repo:    "cli/cli",
		BinPath: "bin",
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
	got, err := generateMiseTOML(ManifestBinary{
		Name:         "gh",
		Backend:      "github",
		Repo:         "cli/cli",
		Version:      "2.40.1",
		AssetPattern: "gh_*_linux_x64.tar.gz",
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
	got, err := generateMiseTOML(ManifestBinary{
		Name:            "pandoc",
		Backend:         "github",
		Repo:            "jgm/pandoc",
		Version:         "3.1.0",
		AssetPattern:    "pandoc-*-linux-amd64.tar.gz",
		VersionPrefix:   "release-",
		StripComponents: 1,
		BinPath:         "bin",
		RenameExe:       "pandoc",
		NoApp:           true,
		FilterBins:      "pandoc",
		Checksum:        "sha256:abc123",
		Prerelease:      true,
		APIURL:          "https://github.example.com/api/v3",
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
	got, err := generateMiseTOML(ManifestBinary{
		Name:    "docker-compose",
		Backend: "github",
		Repo:    "docker/compose",
		Version: "2.29.1",
		Bin:     "docker-compose",
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
	got, err := generateMiseTOML(ManifestBinary{
		Name:    "sentinel",
		Backend: "http",
		URL:     "https://releases.hashicorp.com/sentinel/{{version}}/sentinel_{{version}}_linux_amd64.zip",
		Version: "0.26.3",
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
	got, err := generateMiseTOML(ManifestBinary{
		Name:    "mytool",
		Backend: "http",
		URL:     "https://example.com/mytool-{{version}}-linux-amd64.tar.gz",
		Version: "1.2.0",
		Format:  "tar.gz",
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
	got, err := generateMiseTOML(ManifestBinary{
		Name:    "mypy",
		Backend: "pipx",
		Package: "mypy",
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
	got, err := generateMiseTOML(ManifestBinary{
		Name:    "pylint",
		Backend: "pipx",
		Package: "pylint",
		Extras:  "spelling",
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
	got, err := generateMiseTOML(ManifestBinary{
		Name:    "serve",
		Backend: "npm",
		Package: "serve",
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

func TestGenerateMiseTOMLUnknownBackendErrors(t *testing.T) {
	_, err := generateMiseTOML(ManifestBinary{Name: "x"})
	if err == nil {
		t.Fatal("expected error for binary with no identity fields")
	}
}

func TestInstallBinaryWithMiseIsolatesHostEnvAndTargetsTool(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake mise script uses POSIX shell")
	}

	annaHome := t.TempDir()
	binDir := filepath.Join(annaHome, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("create bin dir: %v", err)
	}

	logPath := filepath.Join(annaHome, "mise.log")
	fakeMise := filepath.Join(binDir, "mise")
	fake := `#!/bin/sh
set -eu
{
  printf 'args:%s\n' "$*"
  printf 'MISE_DATA_DIR=%s\n' "${MISE_DATA_DIR-}"
  printf 'MISE_CONFIG_DIR=%s\n' "${MISE_CONFIG_DIR-}"
  printf 'MISE_CACHE_DIR=%s\n' "${MISE_CACHE_DIR-}"
  printf 'MISE_STATE_DIR=%s\n' "${MISE_STATE_DIR-}"
  printf 'MISE_SHIMS_DIR=%s\n' "${MISE_SHIMS_DIR-}"
  printf 'MISE_PROJECT_ROOT=%s\n' "${MISE_PROJECT_ROOT-}"
  printf 'HOME=%s\n' "${HOME-}"
  printf 'XDG_CONFIG_HOME=%s\n' "${XDG_CONFIG_HOME-}"
} >> ` + shellQuote(logPath) + `
case "$1" in
  trust)
    exit 0
    ;;
  install)
    if [ "$2" != "github:owner/repo" ]; then
      echo "unexpected install target: $*" >&2
      exit 7
    fi
    mkdir -p "$MISE_DATA_DIR/installs/github-owner-repo/1.2.3/bin"
    printf '#!/bin/sh\n' > "$MISE_DATA_DIR/installs/github-owner-repo/1.2.3/bin/mytool"
    chmod +x "$MISE_DATA_DIR/installs/github-owner-repo/1.2.3/bin/mytool"
    exit 0
    ;;
  which)
    # which <name> --version
    if [ "${3:-}" = "--version" ]; then
      printf '1.2.3\n'
      exit 0
    fi
    # which <name>
    printf '%s\n' "$MISE_DATA_DIR/installs/github-owner-repo/1.2.3/bin/$2"
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

	version, err := installBinaryWithMise(context.Background(), ManifestBinary{
		Name:    "mytool",
		Backend: "github",
		Repo:    "owner/repo",
		Version: "1.2.3",
	}, annaHome)
	if err != nil {
		t.Fatalf("installBinaryWithMise: %v", err)
	}
	if version != "1.2.3" {
		t.Fatalf("version = %q, want 1.2.3", version)
	}
	if _, err := os.Stat(filepath.Join(binDir, "mytool")); err != nil {
		t.Fatalf("installed binary missing: %v", err)
	}

	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read fake mise log: %v", err)
	}
	log := string(logData)
	if !strings.Contains(log, "args:install github:owner/repo") {
		t.Fatalf("mise install was not targeted to the manifest tool; log:\n%s", log)
	}
	if !strings.Contains(log, "args:which mytool") {
		t.Fatalf("mise which was not called for the binary; log:\n%s", log)
	}
	if strings.Contains(log, "/danger") {
		t.Fatalf("host mise env leaked into installer; log:\n%s", log)
	}

	wantData := "MISE_DATA_DIR=" + filepath.Join(annaHome, ".mise-tools")
	if !strings.Contains(log, wantData) {
		t.Fatalf("isolated data dir not used, want %q in log:\n%s", wantData, log)
	}
	wantConfig := "MISE_CONFIG_DIR=" + filepath.Join(annaHome, ".mise-tools", "config")
	if !strings.Contains(log, wantConfig) {
		t.Fatalf("isolated config dir not used, want %q in log:\n%s", wantConfig, log)
	}
	if !strings.Contains(log, "MISE_PROJECT_ROOT=\n") {
		t.Fatalf("MISE_PROJECT_ROOT should be stripped; log:\n%s", log)
	}
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
