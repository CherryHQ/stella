package web

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Vite+ deliberately has a global CLI and a local project package. The global
// CLI drives migration and delegates project work to the local toolchain, so a
// split version is two build systems hiding behind the same command.
func TestMiseVPPinMatchesLocalToolchain(t *testing.T) {
	mise, err := os.ReadFile(filepath.Join("..", "mise.toml"))
	if err != nil {
		t.Fatalf("read mise.toml: %v", err)
	}
	pinned := firstSubmatch(t, regexp.MustCompile(`(?m)^vp\s*=\s*"([^"]+)"`), mise, "vp pin in mise.toml")

	workspace, err := os.ReadFile("pnpm-workspace.yaml")
	if err != nil {
		t.Fatalf("read pnpm-workspace.yaml: %v", err)
	}
	for name, re := range map[string]*regexp.Regexp{
		"vite-plus catalog pin": regexp.MustCompile(`(?m)^\s{2}vite-plus:\s*([0-9][^\s]*)\s*$`),
		"Vite core alias pin":   regexp.MustCompile(`(?m)^\s{2}vite:\s*npm:@voidzero-dev/vite-plus-core@([^\s]+)\s*$`),
	} {
		if got := firstSubmatch(t, re, workspace, name); got != pinned {
			t.Errorf("%s = %q, but mise.toml pins vp %q; upgrade the Vite+ toolchain together with vp migrate", name, got, pinned)
		}
	}

	vitest := firstSubmatch(t, regexp.MustCompile(`(?m)^\s{2}vitest:\s*([^\s]+)\s*$`), workspace, "Vitest catalog pin")
	if strings.ContainsAny(vitest, "^~*") || vitest == "latest" {
		t.Errorf("Vitest catalog pin = %q; vp test requires the exact version bundled with the pinned Vite+ release", vitest)
	}

	if regexp.MustCompile(`(?m)^pnpm\s*=`).Match(mise) {
		t.Error("mise.toml must not pin pnpm; package.json#packageManager owns that version and vp downloads it")
	}
}

func TestProjectPinsPackageManager(t *testing.T) {
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
	if !regexp.MustCompile(`^pnpm@[0-9]+\.[0-9]+\.[0-9]+$`).MatchString(pkg.PackageManager) {
		t.Errorf("package.json packageManager = %q; vp requires an exact pnpm version for deterministic installs", pkg.PackageManager)
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
	// Other build-shaping variables (STRIP) may ride along; the contract is that
	// the host GOOS/GOARCH are cleared and the target reaches mise as TARGET_*.
	crossBuild := regexp.MustCompile(`(?m)env\s+-u\s+GOOS\s+-u\s+GOARCH\s+\\?\s*TARGET_GOOS=\$\{TARGETOS\}\s+TARGET_GOARCH=\$\{TARGETARCH\}\s+(?:[A-Za-z_][A-Za-z0-9_]*=\S+\s+)*\\?\s*mise\s+run\s+build\b`)
	if !crossBuild.Match(dockerfile) {
		t.Error("Dockerfile must clear GOOS/GOARCH and pass TARGET_GOOS/TARGET_GOARCH to mise")
	}

	build := readBuildTask(t)
	if !regexp.MustCompile(`(?s)TARGET_GOOS\+x.*TARGET_GOARCH\+x.*TARGET_GOOS:-.*TARGET_GOARCH:-.*must be set together.*exit 1`).Match(build) {
		t.Error("the build task must reject a missing TARGET_GOOS or TARGET_GOARCH")
	}
	if strings.Count(string(build), `GOOS="$TARGET_GOOS"`) != 1 || strings.Count(string(build), `GOARCH="$TARGET_GOARCH"`) != 1 {
		t.Error("TARGET_GOOS/TARGET_GOARCH must map to GOOS/GOARCH exactly once")
	}
	finalBuild := regexp.MustCompile(`(?s)env\s+GOOS="\$TARGET_GOOS"\s+GOARCH="\$TARGET_GOARCH"\s+\\?\s*go\s+build\b`)
	if !finalBuild.Match(build) {
		t.Error("the build task must apply GOOS/GOARCH only to the final go build")
	}
}

// Both images ship a stripped stellad: the symbol and DWARF tables are ~35 MB
// nothing in an image reads, and every pull pays for them. Local builds keep
// their symbols, so the strip is opt-in through STRIP=1 rather than a change to
// the default of `mise run build`.
func TestImagesStripSymbolTables(t *testing.T) {
	build := readBuildTask(t)
	if !regexp.MustCompile(`(?s)STRIP:-0.*LDFLAGS="-s -w \$LDFLAGS"`).Match(build) {
		t.Error("the build task must add -s -w to LDFLAGS when STRIP=1")
	}
	if regexp.MustCompile(`(?m)^\s*LDFLAGS="-s -w -X`).Match(build) {
		t.Error("the build task must not strip by default; local builds need symbols for delve")
	}

	dockerfile, err := os.ReadFile(filepath.Join("..", "Dockerfile"))
	if err != nil {
		t.Fatalf("read Dockerfile: %v", err)
	}
	if !regexp.MustCompile(`(?s)STRIP=1.*mise\s+run\s+build\b`).Match(dockerfile) {
		t.Error("Dockerfile must build with STRIP=1")
	}

	sandbox, err := os.ReadFile(filepath.Join("..", "plugins", "sandbox", "docker", "Dockerfile"))
	if err != nil {
		t.Fatalf("read sandbox Dockerfile: %v", err)
	}
	if !regexp.MustCompile(`-ldflags="-s -w `).Match(sandbox) {
		t.Error("sandbox Dockerfile must build stellad with -s -w")
	}
}

// The `go:embed` directive snapshots web/static/dist at compile time. Without a
// guard in the build task, `mise run build` on a tree that never built the SPA
// produces a binary serving an empty Web UI and reports no error. The guard checks
// presence, not freshness: a rebuild after editing web sources still needs an
// explicit `mise run build:web`.
func TestBuildEnsuresEmbeddedWebUI(t *testing.T) {
	build := readBuildTask(t)
	if !regexp.MustCompile(`\[ -d web/static/dist \] \|\| mise run build:web`).Match(build) {
		t.Error("the build task must build the SPA when web/static/dist is missing")
	}
	if regexp.MustCompile(`(?m)^#MISE depends=\[[^\]]*build:web`).Match(build) {
		t.Error("build must not depend on build:web; it is uncached and would tax every Go rebuild")
	}
}

// The build task is a file task: .mise/tasks/build, named by its path.
func readBuildTask(t *testing.T) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", ".mise", "tasks", "build"))
	if err != nil {
		t.Fatalf("read .mise/tasks/build: %v", err)
	}
	return body
}

// Every builder stage that runs a mise task needs the whole mise config, not
// just mise.toml: task bodies live in .mise/tasks/ and a stage missing them
// fails on the first file task it reaches.
func TestDockerfileCopiesWholeMiseConfig(t *testing.T) {
	dockerfile, err := os.ReadFile(filepath.Join("..", "Dockerfile"))
	if err != nil {
		t.Fatalf("read Dockerfile: %v", err)
	}
	toml := regexp.MustCompile(`(?m)^COPY mise\.toml\b`).FindAll(dockerfile, -1)
	tasks := regexp.MustCompile(`(?m)^COPY \.mise/`).FindAll(dockerfile, -1)
	if len(toml) == 0 {
		t.Fatal("no stage copies mise.toml; this test is watching the wrong thing")
	}
	if len(tasks) != len(toml) {
		t.Errorf("%d stages copy mise.toml but %d copy .mise/; every stage that runs a mise task needs both", len(toml), len(tasks))
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
