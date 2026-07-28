//go:build capability

package capabilities

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadManifestRejectsUnknownField(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "capabilities.yaml")
	writeFixture(t, path, "schema_version: 1\nunknown_field: true\n")

	_, err := LoadManifest(path)
	if err == nil || !strings.Contains(err.Error(), "field unknown_field not found") {
		t.Fatalf("expected unknown field error, got %v", err)
	}
}

func TestCollectRepositorySurfacesUsesPublicIdentifiers(t *testing.T) {
	root := t.TempDir()
	writeRepositoryFixture(t, root)

	surfaces, err := CollectRepositorySurfaces(root, []string{"tool/demo", "tool/demo"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(surfaces.OpenAPI, ","), "getDemo"; got != want {
		t.Fatalf("OpenAPI operations = %q, want %q", got, want)
	}
	if got, want := strings.Join(surfaces.WebRoutes, ","), "/demo"; got != want {
		t.Fatalf("web routes = %q, want %q", got, want)
	}
	if got, want := strings.Join(surfaces.Plugins, ","), "tool/demo"; got != want {
		t.Fatalf("plugins = %q, want %q", got, want)
	}
	if got, want := strings.Join(surfaces.SystemSkills, ","), "demo"; got != want {
		t.Fatalf("system skills = %q, want %q", got, want)
	}
}

func TestCollectRepositorySurfacesUsesRuntimeSkillDiscovery(t *testing.T) {
	root := t.TempDir()
	writeRepositoryFixture(t, root)
	// The runtime recursively discovers skill roots and uses frontmatter for the
	// canonical ID rather than assuming every skill is a direct child directory.
	writeFixture(
		t,
		filepath.Join(root, "resources", "skills", "system", "group", "nested-directory", "SKILL.md"),
		"---\nname: nested-skill\n---\n# Nested\n",
	)

	surfaces, err := CollectRepositorySurfaces(root, []string{"tool/demo"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(surfaces.SystemSkills, ","), "demo,nested-skill"; got != want {
		t.Fatalf("system skills = %q, want %q", got, want)
	}
}

func TestCollectTestMetricsExcludesCapabilityTaggedSelfTests(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, filepath.Join(root, "existing_test.go"), "package demo\n\nimport (\"net/http/httptest\"; \"testing\")\n\nfunc TestExisting(t *testing.T) { _ = httptest.NewRecorder() }\n")
	writeFixture(t, filepath.Join(root, "inventory_test.go"), "//go:build capability\n\npackage demo\n\nimport \"testing\"\n\nfunc TestInventory(t *testing.T) {}\n")

	metrics, err := CollectTestMetrics(root)
	if err != nil {
		t.Fatal(err)
	}
	if metrics.GoTestFiles != 1 || metrics.HTTPTestFiles != 1 {
		t.Fatalf("unexpected metrics: %+v", metrics)
	}
}

func TestValidateAcceptsCompleteFixture(t *testing.T) {
	root := t.TempDir()
	writeRepositoryFixture(t, root)
	manifest := validFixtureManifest()
	surfaces, err := CollectRepositorySurfaces(root, []string{"tool/demo"})
	if err != nil {
		t.Fatal(err)
	}
	surfaces.CLICommands = []string{"stellad demo"}

	if err := Validate(root, manifest, surfaces); err != nil {
		t.Fatal(err)
	}
}

func TestValidateReportsUnmappedSurfaceAndStaleEvidence(t *testing.T) {
	root := t.TempDir()
	writeRepositoryFixture(t, root)
	manifest := validFixtureManifest()
	manifest.Capabilities[0].Surfaces.OpenAPI = nil
	manifest.Capabilities[0].Scenarios[0].Evidence[0].Path = "missing_test.go"
	surfaces, err := CollectRepositorySurfaces(root, []string{"tool/demo"})
	if err != nil {
		t.Fatal(err)
	}
	surfaces.CLICommands = []string{"stellad demo"}

	err = Validate(root, manifest, surfaces)
	if err == nil {
		t.Fatal("expected validation error")
	}
	for _, want := range []string{`openapi surface "getDemo" is not mapped or exempt`, `path "missing_test.go" does not exist`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
}

func TestValidateRequiresKnownReleasePolicy(t *testing.T) {
	root := t.TempDir()
	writeRepositoryFixture(t, root)
	surfaces, err := CollectRepositorySurfaces(root, []string{"tool/demo"})
	if err != nil {
		t.Fatal(err)
	}
	surfaces.CLICommands = []string{"stellad demo"}

	for _, test := range []struct {
		name   string
		policy string
		want   string
	}{
		{name: "missing", want: "requires release_policy"},
		{name: "unknown", policy: "deferred", want: `unknown release_policy "deferred"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			manifest := validFixtureManifest()
			manifest.Capabilities[0].Scenarios[0].ReleasePolicy = test.policy
			err := Validate(root, manifest, surfaces)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected %q, got %v", test.want, err)
			}
		})
	}
}

func TestValidateManualPolicyRequiresManualOnlyStatus(t *testing.T) {
	root := t.TempDir()
	writeRepositoryFixture(t, root)
	manifest := validFixtureManifest()
	manifest.Capabilities[0].Scenarios[0].ReleasePolicy = "manual"
	surfaces, err := CollectRepositorySurfaces(root, []string{"tool/demo"})
	if err != nil {
		t.Fatal(err)
	}
	surfaces.CLICommands = []string{"stellad demo"}

	err = Validate(root, manifest, surfaces)
	if err == nil || !strings.Contains(err.Error(), "with release_policy manual must have status manual-only") {
		t.Fatalf("expected manual policy status error, got %v", err)
	}
}

func TestValidateBrowserTestReference(t *testing.T) {
	root := t.TempDir()
	writeRepositoryFixture(t, root)
	browserPath := filepath.Join(root, "web", "e2e", "release.spec.ts")
	writeFixture(t, browserPath, "import { test } from \"@playwright/test\";\n\ntest(\"[C01-S01] read demo in browser\", async () => {});\n")
	surfaces, err := CollectRepositorySurfaces(root, []string{"tool/demo"})
	if err != nil {
		t.Fatal(err)
	}
	surfaces.CLICommands = []string{"stellad demo"}

	manifest := validFixtureManifest()
	scenario := &manifest.Capabilities[0].Scenarios[0]
	scenario.Layer = "browser"
	scenario.Evidence = []Evidence{{
		Kind:   "browser_test",
		Path:   "web/e2e/release.spec.ts",
		Test:   "[C01-S01] read demo in browser",
		Direct: true,
		Proves: "The browser renders the demo.",
	}}
	if err := Validate(root, manifest, surfaces); err != nil {
		t.Fatalf("valid browser reference: %v", err)
	}

	scenario.Evidence[0].Test = "[C01-S01] stale title"
	err = Validate(root, manifest, surfaces)
	if err == nil || !strings.Contains(err.Error(), `test "[C01-S01] stale title" does not exist`) {
		t.Fatalf("expected stale browser title error, got %v", err)
	}
}

func TestBuildReportKeepsGapsSeparateFromCoveredEvidence(t *testing.T) {
	manifest := validFixtureManifest()
	manifest.Capabilities[0].Scenarios = append(manifest.Capabilities[0].Scenarios, Scenario{
		ID:            "C01-S02",
		Name:          "browser-demo",
		Layer:         "browser",
		Status:        "missing",
		ReleasePolicy: "nonblocking",
		Summary:       "Demo is visible in a browser.",
		Gap:           "No browser runner exists.",
	})
	stamp := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	report := BuildReport(manifest, RepositorySurfaces{OpenAPI: []string{"getDemo"}}, TestMetrics{GoTestFiles: 1}, "abc123", stamp)

	if report.Summary.Scenarios != 2 || report.Summary.ByStatus["covered"] != 1 || report.Summary.ByStatus["missing"] != 1 {
		t.Fatalf("unexpected report summary: %+v", report.Summary)
	}
	if report.Summary.ByPolicy["blocking"] != 1 || report.Summary.ByPolicy["nonblocking"] != 1 ||
		report.Summary.NonblockingBacklog != 1 || report.Summary.BlockingGaps != 0 {
		t.Fatalf("unexpected release policy summary: %+v", report.Summary)
	}
	if len(report.Gaps) != 1 || report.Gaps[0].ScenarioID != "C01-S02" {
		t.Fatalf("unexpected gaps: %+v", report.Gaps)
	}
	if got := renderMarkdown(report); !strings.Contains(got, "## Nonblocking backlog") || !strings.Contains(got, "No browser runner exists.") {
		t.Fatalf("Markdown report does not contain gap: %s", got)
	}
}

func validFixtureManifest() *Manifest {
	return &Manifest{
		SchemaVersion: 1,
		Baseline:      Baseline{Commit: "fixture"},
		TestAssets: []TestAsset{{
			ID:            "A01",
			Category:      "framework",
			Name:          "Go testing",
			Automation:    "automated",
			EvidencePaths: []string{"demo_test.go"},
			Summary:       "Runs the fixture test.",
		}},
		Capabilities: []Capability{{
			ID:          "C01",
			Class:       "core",
			Name:        "demo",
			Title:       "Demo",
			Description: "Fixture capability.",
			Surfaces: SurfaceRefs{
				OpenAPI:      []string{"getDemo"},
				WebRoutes:    []string{"/demo"},
				CLICommands:  []string{"stellad demo"},
				Plugins:      []string{"tool/demo"},
				SystemSkills: []string{"demo"},
			},
			Scenarios: []Scenario{{
				ID:            "C01-S01",
				Name:          "read-demo",
				Layer:         "integration",
				Status:        "covered",
				ReleasePolicy: "blocking",
				Summary:       "The demo can be read.",
				Evidence: []Evidence{{
					Kind:   "go_test",
					Path:   "demo_test.go",
					Test:   "TestDemo",
					Direct: true,
					Proves: "The demo behavior returns successfully.",
				}},
			}},
		}},
	}
}

func writeRepositoryFixture(t *testing.T, root string) {
	t.Helper()
	writeFixture(t, filepath.Join(root, "api", "spec", "domain", "demo", "paths.yaml"), "paths:\n  /demo:\n    get:\n      operationId: getDemo\n")
	writeFixture(t, filepath.Join(root, "web", "src", "routeTree.gen.ts"), "const route = { fullPath: '/demo' }\nconst duplicate = { fullPath: '/demo' }\n")
	writeFixture(t, filepath.Join(root, "resources", "skills", "system", "demo", "SKILL.md"), "# Demo\n")
	writeFixture(t, filepath.Join(root, "demo_test.go"), "package demo\n\nimport \"testing\"\n\nfunc TestDemo(t *testing.T) {}\n")
}

func writeFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
