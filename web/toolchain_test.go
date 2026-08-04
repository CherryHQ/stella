package web

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// mise.toml is the one place the Node and pnpm versions are pinned. pnpm is the
// exception that cannot be: the repository runs pnpm from the root, where there
// is no package.json, so mise has to install a version to bootstrap with — and
// inside web/ pnpm then self-switches to packageManager. A mismatch therefore
// never fails anything, it just means the pinned version is not the one that
// runs, which is how the pin drifted three minor versions out of date unnoticed.
func TestMisePnpmPinMatchesPackageManager(t *testing.T) {
	mise, err := os.ReadFile(filepath.Join("..", "mise.toml"))
	if err != nil {
		t.Fatalf("read mise.toml: %v", err)
	}
	pinned := firstSubmatch(t, regexp.MustCompile(`(?m)^pnpm\s*=\s*"([^"]+)"`), mise, "pnpm pin in mise.toml")

	body, err := os.ReadFile("package.json")
	if err != nil {
		t.Fatalf("read package.json: %v", err)
	}
	var pkg struct {
		PackageManager string `json:"packageManager"`
	}
	if err := json.Unmarshal(body, &pkg); err != nil {
		t.Fatalf("package.json is not valid JSON: %v", err)
	}
	if want := "pnpm@" + pinned; pkg.PackageManager != want {
		t.Errorf("package.json packageManager = %q, but mise.toml pins %q. "+
			"pnpm self-switches to packageManager, so the mise pin would silently stop being the version that runs",
			pkg.PackageManager, want)
	}
}

// Nothing else may pin Node. The release job used to install its own, which is
// how tagged binaries shipped an SPA built on a Node version no pull request
// exercised.
func TestNodeIsPinnedOnlyInMise(t *testing.T) {
	mise, err := os.ReadFile(filepath.Join("..", "mise.toml"))
	if err != nil {
		t.Fatalf("read mise.toml: %v", err)
	}
	firstSubmatch(t, regexp.MustCompile(`(?m)^node\s*=\s*"([^"]+)"`), mise, "node pin in mise.toml")

	// Match the step, not prose: the comment explaining why the step is gone
	// lives in the workflow it was removed from.
	setupNode := regexp.MustCompile(`(?m)^\s*(uses:\s*actions/setup-node|node-version:)`)
	workflows, err := filepath.Glob(filepath.Join("..", ".github", "workflows", "*.yml"))
	if err != nil {
		t.Fatalf("glob workflows: %v", err)
	}
	for _, path := range workflows {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if setupNode.Match(body) {
			t.Errorf("%s sets up its own Node; it would shadow the mise pin on PATH. "+
				"Use jdx/mise-action and run the build through a mise task instead", path)
		}
	}
}

func TestDockerCrossBuildScopesGoTargetToFinalBuild(t *testing.T) {
	dockerfile, err := os.ReadFile(filepath.Join("..", "Dockerfile"))
	if err != nil {
		t.Fatalf("read Dockerfile: %v", err)
	}
	if regexp.MustCompile(`(?s)\b(?:GOOS|GOARCH)=\$\{TARGET(?:OS|ARCH)\}.*?\bmise\s+run\s+build\b`).Match(dockerfile) {
		t.Error("Dockerfile passes GOOS/GOARCH to the entire mise build task graph")
	}
	crossBuild := regexp.MustCompile(`(?m)env\s+-u\s+GOOS\s+-u\s+GOARCH\s+\\?\s*TARGET_GOOS=\$\{TARGETOS\}\s+TARGET_GOARCH=\$\{TARGETARCH\}\s+\\?\s*mise\s+run\s+build\b`)
	if !crossBuild.Match(dockerfile) {
		t.Error("Dockerfile must clear GOOS/GOARCH and pass TARGET_GOOS/TARGET_GOARCH to mise")
	}

	mise, err := os.ReadFile(filepath.Join("..", "mise.toml"))
	if err != nil {
		t.Fatalf("read mise.toml: %v", err)
	}
	if !regexp.MustCompile(`(?s)TARGET_GOOS\+x.*TARGET_GOARCH\+x.*TARGET_GOOS:-.*TARGET_GOARCH:-.*must be set together.*exit 1`).Match(mise) {
		t.Error("mise build must reject a missing TARGET_GOOS or TARGET_GOARCH")
	}
	if strings.Count(string(mise), `GOOS="$TARGET_GOOS"`) != 1 || strings.Count(string(mise), `GOARCH="$TARGET_GOARCH"`) != 1 {
		t.Error("TARGET_GOOS/TARGET_GOARCH must map to GOOS/GOARCH exactly once")
	}
	finalBuild := regexp.MustCompile(`(?s)env\s+GOOS="\$TARGET_GOOS"\s+GOARCH="\$TARGET_GOARCH"\s+\\?\s*go\s+build\b`)
	if !finalBuild.Match(mise) {
		t.Error("mise must apply GOOS/GOARCH only to the final go build")
	}
}

func firstSubmatch(t *testing.T, re *regexp.Regexp, body []byte, what string) string {
	t.Helper()
	m := re.FindSubmatch(body)
	if m == nil {
		t.Fatalf("could not find the %s", what)
	}
	return string(m[1])
}
