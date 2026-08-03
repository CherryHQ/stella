//go:build capability

package capabilities

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Report is the generated, reviewable view of the versioned manifest and the
// surfaces found in one checkout.
type Report struct {
	SchemaVersion int                 `json:"schema_version"`
	InventoryBase string              `json:"inventory_baseline_commit"`
	Checkout      string              `json:"checkout_commit"`
	GeneratedAt   time.Time           `json:"generated_at"`
	Summary       ReportSummary       `json:"summary"`
	Surfaces      []SurfaceReport     `json:"surfaces"`
	Metrics       TestMetrics         `json:"test_metrics"`
	Assets        []TestAsset         `json:"test_assets"`
	Capabilities  []CapabilityReport  `json:"capabilities"`
	Gaps          []ScenarioGapReport `json:"gaps"`
}

// ReportSummary provides counts without treating line or file coverage as
// capability acceptance.
type ReportSummary struct {
	Capabilities       int            `json:"capabilities"`
	Scenarios          int            `json:"scenarios"`
	ByStatus           map[string]int `json:"by_status"`
	ByPolicy           map[string]int `json:"by_policy"`
	DirectRefs         int            `json:"direct_evidence_refs"`
	BlockingGaps       int            `json:"blocking_gaps"`
	NonblockingBacklog int            `json:"nonblocking_backlog"`
	ManualRequirements int            `json:"manual_requirements"`
	LiveScenarios      int            `json:"live_scenarios"`
}

// SurfaceReport compares discovered and manifest-owned surface counts.
type SurfaceReport struct {
	Kind       string `json:"kind"`
	Discovered int    `json:"discovered"`
	Mapped     int    `json:"mapped"`
	Exempt     int    `json:"exempt"`
	Validation string `json:"validation"`
}

// CapabilityReport summarizes scenario evidence for one capability.
type CapabilityReport struct {
	ID         string         `json:"id"`
	Class      string         `json:"class"`
	Name       string         `json:"name"`
	Title      string         `json:"title"`
	Status     string         `json:"status"`
	Scenarios  int            `json:"scenarios"`
	ByStatus   map[string]int `json:"by_status"`
	ByPolicy   map[string]int `json:"by_policy"`
	DirectRefs int            `json:"direct_evidence_refs"`
}

// ScenarioGapReport is one concrete input for the second implementation part.
type ScenarioGapReport struct {
	CapabilityID    string `json:"capability_id"`
	CapabilityTitle string `json:"capability_title"`
	ScenarioID      string `json:"scenario_id"`
	ScenarioName    string `json:"scenario_name"`
	Layer           string `json:"layer"`
	Status          string `json:"status"`
	ReleasePolicy   string `json:"release_policy"`
	Gap             string `json:"gap"`
}

// BuildReport creates a report after Validate has succeeded.
func BuildReport(manifest *Manifest, actual RepositorySurfaces, metrics TestMetrics, checkout string, generatedAt time.Time) Report {
	report := Report{
		SchemaVersion: manifest.SchemaVersion,
		InventoryBase: manifest.Baseline.Commit,
		Checkout:      checkout,
		GeneratedAt:   generatedAt.UTC(),
		Summary: ReportSummary{
			Capabilities: len(manifest.Capabilities),
			ByStatus:     map[string]int{},
			ByPolicy:     map[string]int{},
		},
		Metrics: metrics,
		Assets:  append([]TestAsset(nil), manifest.TestAssets...),
	}

	declared := manifest.DeclaredSurfaces()
	report.Surfaces = []SurfaceReport{
		newSurfaceReport("openapi", len(actual.OpenAPI), len(declared.OpenAPI), len(manifest.SurfaceExemptions.OpenAPI), "checked"),
		newSurfaceReport("web_routes", len(actual.WebRoutes), len(declared.WebRoutes), len(manifest.SurfaceExemptions.WebRoutes), "checked"),
		// The real CLI tree is validated by the tagged cmd/stellad test in the
		// same mise task; package main cannot be imported by this report command.
		newSurfaceReport("cli_commands", len(declared.CLICommands), len(declared.CLICommands), len(manifest.SurfaceExemptions.CLICommands), "checked-by-cmd-test"),
		newSurfaceReport("plugins", len(actual.Plugins), len(declared.Plugins), len(manifest.SurfaceExemptions.Plugins), "checked"),
		newSurfaceReport("system_skills", len(actual.SystemSkills), len(declared.SystemSkills), len(manifest.SurfaceExemptions.SystemSkills), "checked"),
		newSurfaceReport("builtin_souls", len(actual.BuiltinSouls), len(declared.BuiltinSouls), len(manifest.SurfaceExemptions.BuiltinSouls), "checked"),
		newSurfaceReport("builtin_delegates", len(actual.BuiltinDelegates), len(declared.BuiltinDelegates), len(manifest.SurfaceExemptions.BuiltinDelegates), "checked"),
		newSurfaceReport("builtin_templates", len(actual.BuiltinTemplates), len(declared.BuiltinTemplates), len(manifest.SurfaceExemptions.BuiltinTemplates), "checked"),
		newSurfaceReport("core_tools", len(actual.CoreTools), len(declared.CoreTools), len(manifest.SurfaceExemptions.CoreTools), "checked"),
		newSurfaceReport("system_journeys", len(actual.SystemJourneys), len(sortedUnique(manifest.DeclaredSystemJourneys())), 0, "checked-by-evidence"),
	}

	for _, capability := range manifest.Capabilities {
		item := CapabilityReport{
			ID:        capability.ID,
			Class:     capability.Class,
			Name:      capability.Name,
			Title:     capability.Title,
			Scenarios: len(capability.Scenarios),
			ByStatus:  map[string]int{},
			ByPolicy:  map[string]int{},
		}
		for _, scenario := range capability.Scenarios {
			report.Summary.Scenarios++
			report.Summary.ByStatus[scenario.Status]++
			report.Summary.ByPolicy[scenario.ReleasePolicy]++
			item.ByStatus[scenario.Status]++
			item.ByPolicy[scenario.ReleasePolicy]++
			if scenario.Layer == "live" {
				report.Summary.LiveScenarios++
			}
			for _, evidence := range scenario.Evidence {
				if evidence.Direct {
					report.Summary.DirectRefs++
					item.DirectRefs++
				}
			}
			if scenario.Status != "covered" {
				switch scenario.ReleasePolicy {
				case "blocking":
					report.Summary.BlockingGaps++
				case "nonblocking":
					report.Summary.NonblockingBacklog++
				case "manual":
					report.Summary.ManualRequirements++
				}
				report.Gaps = append(report.Gaps, ScenarioGapReport{
					CapabilityID:    capability.ID,
					CapabilityTitle: capability.Title,
					ScenarioID:      scenario.ID,
					ScenarioName:    scenario.Name,
					Layer:           scenario.Layer,
					Status:          scenario.Status,
					ReleasePolicy:   scenario.ReleasePolicy,
					Gap:             scenario.Gap,
				})
			}
		}
		item.Status = aggregateCapabilityStatus(item.ByStatus)
		report.Capabilities = append(report.Capabilities, item)
	}

	sort.Slice(report.Assets, func(i, j int) bool { return report.Assets[i].ID < report.Assets[j].ID })
	sort.Slice(report.Capabilities, func(i, j int) bool { return report.Capabilities[i].ID < report.Capabilities[j].ID })
	sort.Slice(report.Gaps, func(i, j int) bool { return report.Gaps[i].ScenarioID < report.Gaps[j].ScenarioID })
	return report
}

func newSurfaceReport(kind string, discovered, mapped, exempt int, validation string) SurfaceReport {
	return SurfaceReport{Kind: kind, Discovered: discovered, Mapped: mapped, Exempt: exempt, Validation: validation}
}

func aggregateCapabilityStatus(counts map[string]int) string {
	if counts["missing"] > 0 {
		return "missing"
	}
	if counts["manual-only"] > 0 {
		return "manual-only"
	}
	if counts["partial"] > 0 {
		return "partial"
	}
	return "covered"
}

// WriteReport writes deterministic JSON and Markdown filenames below outputDir.
func WriteReport(outputDir string, report Report) (string, string, error) {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return "", "", fmt.Errorf("create report directory: %w", err)
	}
	jsonPath := filepath.Join(outputDir, "inventory.json")
	markdownPath := filepath.Join(outputDir, "inventory.md")
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return "", "", fmt.Errorf("encode JSON report: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(jsonPath, data, 0o644); err != nil {
		return "", "", fmt.Errorf("write JSON report: %w", err)
	}
	if err := os.WriteFile(markdownPath, []byte(renderMarkdown(report)), 0o644); err != nil {
		return "", "", fmt.Errorf("write Markdown report: %w", err)
	}
	return jsonPath, markdownPath, nil
}

func renderMarkdown(report Report) string {
	var out strings.Builder
	fmt.Fprintln(&out, "# Stella Capability Inventory")
	fmt.Fprintln(&out)
	fmt.Fprintf(&out, "- Checkout: `%s`\n", markdownCell(report.Checkout))
	fmt.Fprintf(&out, "- Inventory baseline: `%s`\n", markdownCell(report.InventoryBase))
	fmt.Fprintf(&out, "- Generated: `%s`\n", report.GeneratedAt.Format(time.RFC3339))
	fmt.Fprintf(&out, "- Capabilities: %d; scenarios: %d; gaps: %d\n", report.Summary.Capabilities, report.Summary.Scenarios, len(report.Gaps))
	fmt.Fprintf(&out, "- Release policy: blocking %d; nonblocking %d; manual %d\n",
		report.Summary.ByPolicy["blocking"],
		report.Summary.ByPolicy["nonblocking"],
		report.Summary.ByPolicy["manual"],
	)
	fmt.Fprintf(&out, "- Open items: blocking gaps %d; nonblocking backlog %d; manual requirements %d; registered live scenarios %d\n",
		report.Summary.BlockingGaps,
		report.Summary.NonblockingBacklog,
		report.Summary.ManualRequirements,
		report.Summary.LiveScenarios,
	)
	fmt.Fprintln(&out)

	fmt.Fprintln(&out, "## Surface coverage")
	fmt.Fprintln(&out)
	fmt.Fprintln(&out, "| Surface | Discovered | Mapped | Exempt | Validation |")
	fmt.Fprintln(&out, "| --- | ---: | ---: | ---: | --- |")
	for _, surface := range report.Surfaces {
		fmt.Fprintf(&out, "| %s | %d | %d | %d | %s |\n", surface.Kind, surface.Discovered, surface.Mapped, surface.Exempt, surface.Validation)
	}
	fmt.Fprintln(&out)

	fmt.Fprintln(&out, "## Existing test assets")
	fmt.Fprintln(&out)
	fmt.Fprintf(&out, "Go test files: %d; frontend test files: %d; system test files: %d; httptest users: %d; dbtest users: %d; memorytest users: %d.\n",
		report.Metrics.GoTestFiles,
		report.Metrics.FrontendTestFiles,
		report.Metrics.SystemTestFiles,
		report.Metrics.HTTPTestFiles,
		report.Metrics.DBTestFiles,
		report.Metrics.MemoryTestFiles,
	)
	fmt.Fprintln(&out)
	fmt.Fprintln(&out, "| ID | Category | Asset | Automation | Scope | Limitation |")
	fmt.Fprintln(&out, "| --- | --- | --- | --- | --- | --- |")
	for _, asset := range report.Assets {
		fmt.Fprintf(&out, "| %s | %s | %s | %s | %s | %s |\n",
			asset.ID,
			markdownCell(asset.Category),
			markdownCell(asset.Name),
			markdownCell(asset.Automation),
			markdownCell(asset.Summary),
			markdownCell(asset.Limitations),
		)
	}
	fmt.Fprintln(&out)

	fmt.Fprintln(&out, "## Capability status")
	fmt.Fprintln(&out)
	fmt.Fprintln(&out, "| ID | Class | Capability | Status | Scenarios | Covered | Partial | Missing | Manual only | Blocking | Nonblocking | Manual | Direct refs |")
	fmt.Fprintln(&out, "| --- | --- | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |")
	for _, capability := range report.Capabilities {
		fmt.Fprintf(&out, "| %s | %s | %s | %s | %d | %d | %d | %d | %d | %d | %d | %d | %d |\n",
			capability.ID,
			capability.Class,
			markdownCell(capability.Title),
			capability.Status,
			capability.Scenarios,
			capability.ByStatus["covered"],
			capability.ByStatus["partial"],
			capability.ByStatus["missing"],
			capability.ByStatus["manual-only"],
			capability.ByPolicy["blocking"],
			capability.ByPolicy["nonblocking"],
			capability.ByPolicy["manual"],
			capability.DirectRefs,
		)
	}
	fmt.Fprintln(&out)

	if len(report.Gaps) == 0 {
		fmt.Fprintln(&out, "## Gap report")
		fmt.Fprintln(&out)
		fmt.Fprintln(&out, "No gaps recorded.")
		return out.String()
	}
	for _, section := range []struct {
		Title  string
		Policy string
	}{
		{Title: "Blocking gaps", Policy: "blocking"},
		{Title: "Nonblocking backlog", Policy: "nonblocking"},
		{Title: "Manual requirements", Policy: "manual"},
	} {
		fmt.Fprintf(&out, "## %s\n\n", section.Title)
		fmt.Fprintln(&out, "| Scenario | Capability | Layer | Status | Gap |")
		fmt.Fprintln(&out, "| --- | --- | --- | --- | --- |")
		wrote := false
		for _, gap := range report.Gaps {
			if gap.ReleasePolicy != section.Policy {
				continue
			}
			wrote = true
			fmt.Fprintf(&out, "| %s | %s %s | %s | %s | %s |\n",
				gap.ScenarioID,
				gap.CapabilityID,
				markdownCell(gap.CapabilityTitle),
				gap.Layer,
				gap.Status,
				markdownCell(gap.Gap),
			)
		}
		if !wrote {
			fmt.Fprintln(&out, "| _None_ |  |  |  |  |")
		}
		fmt.Fprintln(&out)
	}
	return out.String()
}

func markdownCell(value string) string {
	value = strings.ReplaceAll(value, "|", "\\|")
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	return value
}
