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

func TestValidateRejectsOldSchemaVersion(t *testing.T) {
	root := t.TempDir()
	writeRepositoryFixture(t, root)
	manifest := validFixtureManifest()
	manifest.SchemaVersion = 1
	surfaces, err := CollectRepositorySurfaces(root, []string{"tool/demo"})
	if err != nil {
		t.Fatal(err)
	}
	surfaces.CLICommands = []string{"stellad demo"}

	err = Validate(root, manifest, surfaces)
	if err == nil || !strings.Contains(err.Error(), "schema_version must be 2, got 1") {
		t.Fatalf("expected old schema version error, got %v", err)
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
	if got, want := strings.Join(surfaces.BuiltinSouls, ","), "demo"; got != want {
		t.Fatalf("souls = %q, want %q", got, want)
	}
	if got, want := strings.Join(surfaces.BuiltinDelegates, ","), "demo"; got != want {
		t.Fatalf("delegates = %q, want %q", got, want)
	}
	if got, want := strings.Join(surfaces.BuiltinTemplates, ","), "demo"; got != want {
		t.Fatalf("templates = %q, want %q", got, want)
	}
	if got, want := strings.Join(surfaces.CoreTools, ","), "bash,edit,read,write"; got != want {
		t.Fatalf("core tools = %q, want %q", got, want)
	}
	if got, want := strings.Join(surfaces.SystemJourneys, ","), "demo_journey"; got != want {
		t.Fatalf("system journeys = %q, want %q", got, want)
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

func TestCollectRepositorySurfacesCountsOnlyTopLevelSystemJourneys(t *testing.T) {
	root := t.TempDir()
	writeRepositoryFixture(t, root)
	writeFixture(t, filepath.Join(root, "test", "system", "system_test.go"), `package system

import "testing"

func TestSystem(t *testing.T) {
	t.Run("outer", func(t *testing.T) {
		t.Run("nested", func(t *testing.T) {})
	})
}
`)

	surfaces, err := CollectRepositorySurfaces(root, []string{"tool/demo"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(surfaces.SystemJourneys, ","), "outer"; got != want {
		t.Fatalf("system journeys = %q, want %q", got, want)
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

func TestValidateTracksBuiltinResourceAndCoreToolSurfaces(t *testing.T) {
	root := t.TempDir()
	writeRepositoryFixture(t, root)
	surfaces, err := CollectRepositorySurfaces(root, []string{"tool/demo"})
	if err != nil {
		t.Fatal(err)
	}
	surfaces.CLICommands = []string{"stellad demo"}

	for _, test := range []struct {
		name  string
		clear func(*SurfaceRefs)
		want  string
	}{
		{name: "builtin soul", clear: func(s *SurfaceRefs) { s.BuiltinSouls = nil }, want: `builtin_souls surface "demo" is not mapped or exempt`},
		{name: "builtin delegate", clear: func(s *SurfaceRefs) { s.BuiltinDelegates = nil }, want: `builtin_delegates surface "demo" is not mapped or exempt`},
		{name: "builtin template", clear: func(s *SurfaceRefs) { s.BuiltinTemplates = nil }, want: `builtin_templates surface "demo" is not mapped or exempt`},
		{name: "core tool", clear: func(s *SurfaceRefs) { s.CoreTools = nil }, want: `core_tools surface "bash" is not mapped or exempt`},
	} {
		t.Run(test.name, func(t *testing.T) {
			manifest := validFixtureManifest()
			test.clear(&manifest.Capabilities[0].Surfaces)
			err := Validate(root, manifest, surfaces)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected %q, got %v", test.want, err)
			}
		})
	}
}

func TestValidateNewSurfaceOwnershipInvariants(t *testing.T) {
	root := t.TempDir()
	writeRepositoryFixture(t, root)
	surfaces, err := CollectRepositorySurfaces(root, []string{"tool/demo"})
	if err != nil {
		t.Fatal(err)
	}
	surfaces.CLICommands = []string{"stellad demo"}

	for _, test := range []struct {
		name   string
		mutate func(*Manifest)
		want   string
	}{
		{
			name: "stale mapping",
			mutate: func(manifest *Manifest) {
				manifest.Capabilities[0].Surfaces.BuiltinSouls = append(manifest.Capabilities[0].Surfaces.BuiltinSouls, "retired")
			},
			want: `builtin_souls mapped surface "retired" no longer exists`,
		},
		{
			name: "duplicate owner",
			mutate: func(manifest *Manifest) {
				manifest.Capabilities[0].Surfaces.BuiltinSouls = append(manifest.Capabilities[0].Surfaces.BuiltinSouls, "demo")
			},
			want: `builtin_souls surface "demo" is mapped more than once`,
		},
		{
			name: "mapped and exempt",
			mutate: func(manifest *Manifest) {
				manifest.SurfaceExemptions.BuiltinSouls = []Exemption{{ID: "demo", Reason: "fixture"}}
			},
			want: `builtin_souls surface "demo" cannot be both mapped and exempt`,
		},
		{
			name: "exemption reason",
			mutate: func(manifest *Manifest) {
				manifest.Capabilities[0].Surfaces.BuiltinSouls = nil
				manifest.SurfaceExemptions.BuiltinSouls = []Exemption{{ID: "demo"}}
			},
			want: `builtin_souls exemption "demo" requires id and reason`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			manifest := validFixtureManifest()
			test.mutate(manifest)
			err := Validate(root, manifest, surfaces)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected %q, got %v", test.want, err)
			}
		})
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

func TestValidateRejectsBlockingLiveScenario(t *testing.T) {
	root := t.TempDir()
	writeRepositoryFixture(t, root)
	manifest := validFixtureManifest()
	manifest.Capabilities[0].Scenarios[0].Layer = "live"
	surfaces, err := CollectRepositorySurfaces(root, []string{"tool/demo"})
	if err != nil {
		t.Fatal(err)
	}
	surfaces.CLICommands = []string{"stellad demo"}

	err = Validate(root, manifest, surfaces)
	if err == nil || !strings.Contains(err.Error(), "live scenario C01-S01 cannot be blocking") {
		t.Fatalf("expected blocking Live policy error, got %v", err)
	}
}

func TestValidateRequiresEverySystemJourneyAsScenarioEvidence(t *testing.T) {
	root := t.TempDir()
	writeRepositoryFixture(t, root)
	manifest := validFixtureManifest()
	manifest.Capabilities[0].Scenarios[0].Evidence = manifest.Capabilities[0].Scenarios[0].Evidence[:1]
	surfaces, err := CollectRepositorySurfaces(root, []string{"tool/demo"})
	if err != nil {
		t.Fatal(err)
	}
	surfaces.CLICommands = []string{"stellad demo"}

	err = Validate(root, manifest, surfaces)
	if err == nil || !strings.Contains(err.Error(), `system_journeys evidence "demo_journey" is not mapped`) {
		t.Fatalf("expected unmapped system journey error, got %v", err)
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
	report := BuildReport(manifest, RepositorySurfaces{
		OpenAPI:          []string{"getDemo"},
		BuiltinSouls:     []string{"demo"},
		BuiltinDelegates: []string{"demo"},
		BuiltinTemplates: []string{"demo"},
		CoreTools:        []string{"bash", "edit", "read", "write"},
		SystemJourneys:   []string{"demo_journey"},
	}, TestMetrics{GoTestFiles: 1}, "abc123", stamp)

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
	for _, want := range []struct {
		kind       string
		discovered int
		mapped     int
	}{
		{kind: "builtin_souls", discovered: 1, mapped: 1},
		{kind: "builtin_delegates", discovered: 1, mapped: 1},
		{kind: "builtin_templates", discovered: 1, mapped: 1},
		{kind: "core_tools", discovered: 4, mapped: 4},
		{kind: "system_journeys", discovered: 1, mapped: 1},
	} {
		var found *SurfaceReport
		for i := range report.Surfaces {
			if report.Surfaces[i].Kind == want.kind {
				found = &report.Surfaces[i]
				break
			}
		}
		if found == nil || found.Discovered != want.discovered || found.Mapped != want.mapped {
			t.Fatalf("surface %s = %+v, want discovered=%d mapped=%d", want.kind, found, want.discovered, want.mapped)
		}
	}
	if got := renderMarkdown(report); !strings.Contains(got, "## Nonblocking backlog") || !strings.Contains(got, "No browser runner exists.") {
		t.Fatalf("Markdown report does not contain gap: %s", got)
	}
}

func validFixtureManifest() *Manifest {
	return &Manifest{
		SchemaVersion: 2,
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
				OpenAPI:          []string{"getDemo"},
				WebRoutes:        []string{"/demo"},
				CLICommands:      []string{"stellad demo"},
				Plugins:          []string{"tool/demo"},
				SystemSkills:     []string{"demo"},
				BuiltinSouls:     []string{"demo"},
				BuiltinDelegates: []string{"demo"},
				BuiltinTemplates: []string{"demo"},
				CoreTools:        []string{"bash", "edit", "read", "write"},
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
				}, {
					Kind:    "system_test",
					Path:    "test/system/system_test.go",
					Test:    "TestSystem",
					Subtest: "demo_journey",
					Direct:  true,
					Proves:  "The demo journey runs through the real process.",
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
	writeFixture(t, filepath.Join(root, "resources", "souls", "demo.md"), "# Demo\n")
	writeFixture(t, filepath.Join(root, "resources", "delegates", "demo.md"), "# Demo\n")
	writeFixture(t, filepath.Join(root, "resources", "templates", "demo.md"), "# Demo\n")
	writeFixture(t, filepath.Join(root, "demo_test.go"), "package demo\n\nimport \"testing\"\n\nfunc TestDemo(t *testing.T) {}\n")
	writeFixture(t, filepath.Join(root, "test", "system", "system_test.go"), "package system\n\nimport \"testing\"\n\nfunc TestSystem(t *testing.T) { t.Run(\"demo_journey\", func(t *testing.T) {}) }\n")
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
