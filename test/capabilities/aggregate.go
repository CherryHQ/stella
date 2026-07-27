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

	releasecontract "github.com/CherryHQ/stella/test/release"
)

const (
	gateReportSchemaVersion = 1
	missingResultStatus     = "missing_result"
)

// GateReport is the complete release decision for one immutable candidate.
type GateReport struct {
	SchemaVersion  int                 `json:"schema_version"`
	GeneratedAt    time.Time           `json:"generated_at"`
	Target         releasecontract.Run `json:"target"`
	ReleaseAllowed bool                `json:"release_allowed"`
	Summary        GateSummary         `json:"summary"`
	Scenarios      []ScenarioDecision  `json:"scenarios"`
	Blockers       []GateBlocker       `json:"blockers,omitempty"`
}

// GateSummary provides stable counts for release workflow annotations.
type GateSummary struct {
	Scenarios           int            `json:"scenarios"`
	ResultRecords       int            `json:"result_records"`
	IgnoredStaleResults int            `json:"ignored_stale_results"`
	Blockers            int            `json:"blockers"`
	ByStatus            map[string]int `json:"by_status"`
	ByPolicy            map[string]int `json:"by_policy"`
}

// ScenarioDecision is the effective current-Run outcome for one manifest
// Scenario after retries and policy are applied.
type ScenarioDecision struct {
	CapabilityID     string                     `json:"capability_id"`
	CapabilityTitle  string                     `json:"capability_title"`
	ScenarioID       string                     `json:"scenario_id"`
	ScenarioName     string                     `json:"scenario_name"`
	Layer            string                     `json:"layer"`
	ReleasePolicy    string                     `json:"release_policy"`
	Status           string                     `json:"status"`
	Attempt          int                        `json:"attempt,omitempty"`
	AttemptsRecorded int                        `json:"attempts_recorded"`
	Runner           *releasecontract.Runner    `json:"runner,omitempty"`
	Platforms        []releasecontract.Platform `json:"platforms,omitempty"`
	Reason           string                     `json:"reason,omitempty"`
	Allowed          bool                       `json:"allowed"`
	Decision         string                     `json:"decision"`
	Artifacts        []releasecontract.Artifact `json:"artifacts,omitempty"`
	Waiver           *releasecontract.Waiver    `json:"waiver,omitempty"`
}

// GateBlocker is one actionable reason Promotion cannot run.
type GateBlocker struct {
	CapabilityID string `json:"capability_id"`
	ScenarioID   string `json:"scenario_id"`
	Status       string `json:"status"`
	Reason       string `json:"reason"`
}

type scenarioSpec struct {
	capability Capability
	scenario   Scenario
}

// BuildGateReport combines current-Run results with the long-lived capability
// manifest. Results from another Run ID are counted but cannot influence the
// decision; reuse of the current Run ID for another candidate is a hard error.
func BuildGateReport(
	repositoryRoot string,
	manifest *Manifest,
	target releasecontract.Run,
	results []releasecontract.Result,
	generatedAt time.Time,
) (GateReport, error) {
	if manifest == nil {
		return GateReport{}, fmt.Errorf("capability manifest is nil")
	}
	if err := target.Validate(); err != nil {
		return GateReport{}, fmt.Errorf("validate release target: %w", err)
	}

	specs := make(map[string]scenarioSpec)
	var scenarioIDs []string
	for _, capability := range manifest.Capabilities {
		for _, scenario := range capability.Scenarios {
			if _, exists := specs[scenario.ID]; exists {
				return GateReport{}, fmt.Errorf("capability manifest repeats scenario %s", scenario.ID)
			}
			switch scenario.ReleasePolicy {
			case "blocking", "nonblocking", "manual":
			default:
				return GateReport{}, fmt.Errorf("scenario %s has unsupported release policy %q", scenario.ID, scenario.ReleasePolicy)
			}
			specs[scenario.ID] = scenarioSpec{capability: capability, scenario: scenario}
			scenarioIDs = append(scenarioIDs, scenario.ID)
		}
	}
	sort.Strings(scenarioIDs)

	report := GateReport{
		SchemaVersion:  gateReportSchemaVersion,
		GeneratedAt:    generatedAt.UTC(),
		Target:         target,
		ReleaseAllowed: true,
		Summary: GateSummary{
			Scenarios: len(scenarioIDs),
			ByStatus:  map[string]int{},
			ByPolicy:  map[string]int{},
		},
	}
	grouped := make(map[string][]releasecontract.Result)
	for i, result := range results {
		if err := result.Validate(); err != nil {
			return GateReport{}, fmt.Errorf("results[%d]: %w", i, err)
		}
		if result.Run.ID != target.ID {
			report.Summary.IgnoredStaleResults++
			continue
		}
		if result.Run.Version != target.Version || result.Run.Commit != target.Commit {
			return GateReport{}, fmt.Errorf(
				"result %s attempt %d reuses run id %s for candidate %s@%s instead of %s@%s",
				result.ScenarioID,
				result.Attempt,
				target.ID,
				result.Run.Version,
				result.Run.Commit,
				target.Version,
				target.Commit,
			)
		}
		spec, exists := specs[result.ScenarioID]
		if !exists {
			return GateReport{}, fmt.Errorf("current run contains unknown scenario %s", result.ScenarioID)
		}
		if result.CapabilityID != spec.capability.ID {
			return GateReport{}, fmt.Errorf(
				"scenario %s result capability %s does not match manifest capability %s",
				result.ScenarioID,
				result.CapabilityID,
				spec.capability.ID,
			)
		}
		if err := releasecontract.ValidateArtifactFiles(repositoryRoot, result); err != nil {
			return GateReport{}, fmt.Errorf("scenario %s attempt %d: %w", result.ScenarioID, result.Attempt, err)
		}
		grouped[result.ScenarioID] = append(grouped[result.ScenarioID], result)
		report.Summary.ResultRecords++
	}

	for _, scenarioID := range scenarioIDs {
		spec := specs[scenarioID]
		report.Summary.ByPolicy[spec.scenario.ReleasePolicy]++
		decision, err := buildScenarioDecision(spec, grouped[scenarioID])
		if err != nil {
			return GateReport{}, err
		}
		report.Scenarios = append(report.Scenarios, decision)
		report.Summary.ByStatus[decision.Status]++
		if !decision.Allowed {
			report.ReleaseAllowed = false
			report.Blockers = append(report.Blockers, GateBlocker{
				CapabilityID: decision.CapabilityID,
				ScenarioID:   decision.ScenarioID,
				Status:       decision.Status,
				Reason:       decision.Decision,
			})
		}
	}
	report.Summary.Blockers = len(report.Blockers)
	return report, nil
}

func buildScenarioDecision(spec scenarioSpec, results []releasecontract.Result) (ScenarioDecision, error) {
	decision := ScenarioDecision{
		CapabilityID:     spec.capability.ID,
		CapabilityTitle:  spec.capability.Title,
		ScenarioID:       spec.scenario.ID,
		ScenarioName:     spec.scenario.Name,
		Layer:            spec.scenario.Layer,
		ReleasePolicy:    spec.scenario.ReleasePolicy,
		AttemptsRecorded: len(results),
	}
	if len(results) == 0 {
		decision.Status = missingResultStatus
		decision.Decision = "no result exists for the current release run"
		return decision, nil
	}

	sort.Slice(results, func(i, j int) bool { return results[i].Attempt < results[j].Attempt })
	for i := 1; i < len(results); i++ {
		if results[i-1].Attempt == results[i].Attempt {
			return ScenarioDecision{}, fmt.Errorf(
				"scenario %s contains duplicate attempt %d",
				spec.scenario.ID,
				results[i].Attempt,
			)
		}
	}
	effective := results[len(results)-1]
	for _, result := range results {
		if result.Status == releasecontract.StatusProductFailure {
			// A product failure is sticky for this immutable candidate. A later
			// retry or waiver cannot erase it; a code fix requires a new commit.
			effective = result
		}
	}

	runner := effective.Runner
	decision.Attempt = effective.Attempt
	decision.Runner = &runner
	decision.Platforms = append([]releasecontract.Platform(nil), effective.Platforms...)
	sort.Slice(decision.Platforms, func(i, j int) bool {
		if decision.Platforms[i].OS != decision.Platforms[j].OS {
			return decision.Platforms[i].OS < decision.Platforms[j].OS
		}
		return decision.Platforms[i].Arch < decision.Platforms[j].Arch
	})
	decision.Status = string(effective.Status)
	decision.Reason = effective.Reason
	decision.Artifacts = append([]releasecontract.Artifact(nil), effective.Artifacts...)
	sort.Slice(decision.Artifacts, func(i, j int) bool {
		return decision.Artifacts[i].Path < decision.Artifacts[j].Path
	})
	decision.Waiver = effective.Waiver
	decision.Allowed, decision.Decision = applyReleasePolicy(spec.scenario.ReleasePolicy, effective)
	return decision, nil
}

func applyReleasePolicy(policy string, result releasecontract.Result) (bool, string) {
	switch result.Status {
	case releasecontract.StatusPass:
		return true, "scenario passed"
	case releasecontract.StatusProductFailure:
		return false, fmt.Sprintf("product failure in attempt %d cannot be waived: %s", result.Attempt, result.Reason)
	case releasecontract.StatusWaived:
		return true, fmt.Sprintf(
			"%s was waived by %s for this commit: %s",
			result.Waiver.OriginalStatus,
			result.Waiver.Approver,
			result.Waiver.Reason,
		)
	case releasecontract.StatusExternalBlocked, releasecontract.StatusNotRun, releasecontract.StatusFlaky:
		if policy == "nonblocking" {
			return true, fmt.Sprintf("recorded nonblocking %s: %s", result.Status, result.Reason)
		}
		return false, fmt.Sprintf("%s requires an explicit waiver: %s", result.Status, result.Reason)
	case releasecontract.StatusManualPending:
		return false, fmt.Sprintf("manual release check is pending: %s", result.Reason)
	default:
		return false, fmt.Sprintf("unsupported status %s", result.Status)
	}
}

// WriteGateReport writes release.json and release.md after scanning both
// generated representations for configured secret canaries.
func WriteGateReport(outputDir string, report GateReport, secrets map[string]string) (string, string, error) {
	jsonData, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return "", "", fmt.Errorf("encode release gate JSON: %w", err)
	}
	jsonData = append(jsonData, '\n')
	markdownData := []byte(renderGateMarkdown(report))
	if err := releasecontract.CheckBytesForSecrets("release.json", jsonData, secrets); err != nil {
		return "", "", err
	}
	if err := releasecontract.CheckBytesForSecrets("release.md", markdownData, secrets); err != nil {
		return "", "", err
	}
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return "", "", fmt.Errorf("create release report directory: %w", err)
	}
	jsonPath := filepath.Join(outputDir, "release.json")
	markdownPath := filepath.Join(outputDir, "release.md")
	if err := writeOwnedReport(jsonPath, jsonData); err != nil {
		return "", "", err
	}
	if err := writeOwnedReport(markdownPath, markdownData); err != nil {
		return "", "", err
	}
	return jsonPath, markdownPath, nil
}

func writeOwnedReport(path string, data []byte) error {
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, ".release-report-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary report for %s: %w", path, err)
	}
	tempPath := temp.Name()
	defer func() { _ = os.Remove(tempPath) }()
	if err := temp.Chmod(0o644); err != nil {
		_ = temp.Close()
		return fmt.Errorf("set report permissions for %s: %w", path, err)
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return fmt.Errorf("write temporary report for %s: %w", path, err)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return fmt.Errorf("sync temporary report for %s: %w", path, err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close temporary report for %s: %w", path, err)
	}
	// The aggregate report is derived first-party output owned by this command,
	// so replacing an earlier report for the same Run is intentional.
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("install release report %s: %w", path, err)
	}
	return nil
}

func renderGateMarkdown(report GateReport) string {
	var out strings.Builder
	fmt.Fprintln(&out, "# Stella Release Full Test")
	fmt.Fprintln(&out)
	fmt.Fprintf(&out, "- Run: `%s`\n", markdownCell(report.Target.ID))
	fmt.Fprintf(&out, "- Candidate: `%s` at `%s`\n", markdownCell(report.Target.Version), markdownCell(report.Target.Commit))
	fmt.Fprintf(&out, "- Generated: `%s`\n", report.GeneratedAt.Format(time.RFC3339))
	fmt.Fprintf(&out, "- Release allowed: **%t**\n", report.ReleaseAllowed)
	fmt.Fprintf(
		&out,
		"- Scenarios: %d; result records: %d; blockers: %d; ignored stale results: %d\n",
		report.Summary.Scenarios,
		report.Summary.ResultRecords,
		report.Summary.Blockers,
		report.Summary.IgnoredStaleResults,
	)
	fmt.Fprintln(&out)

	fmt.Fprintln(&out, "## Scenario decisions")
	fmt.Fprintln(&out)
	fmt.Fprintln(&out, "| Scenario | Capability | Layer | Policy | Status | Attempt | Platforms | Allowed | Decision |")
	fmt.Fprintln(&out, "| --- | --- | --- | --- | --- | ---: | --- | --- | --- |")
	for _, decision := range report.Scenarios {
		platforms := make([]string, 0, len(decision.Platforms))
		for _, platform := range decision.Platforms {
			platforms = append(platforms, platform.OS+"/"+platform.Arch)
		}
		attempt := ""
		if decision.Attempt > 0 {
			attempt = fmt.Sprintf("%d", decision.Attempt)
		}
		fmt.Fprintf(
			&out,
			"| %s | %s %s | %s | %s | %s | %s | %s | %t | %s |\n",
			decision.ScenarioID,
			decision.CapabilityID,
			markdownCell(decision.CapabilityTitle),
			decision.Layer,
			decision.ReleasePolicy,
			decision.Status,
			attempt,
			markdownCell(strings.Join(platforms, ", ")),
			decision.Allowed,
			markdownCell(decision.Decision),
		)
	}
	fmt.Fprintln(&out)

	fmt.Fprintln(&out, "## Promotion blockers")
	fmt.Fprintln(&out)
	if len(report.Blockers) == 0 {
		fmt.Fprintln(&out, "None.")
		return out.String()
	}
	fmt.Fprintln(&out, "| Scenario | Status | Reason |")
	fmt.Fprintln(&out, "| --- | --- | --- |")
	for _, blocker := range report.Blockers {
		fmt.Fprintf(
			&out,
			"| %s | %s | %s |\n",
			blocker.ScenarioID,
			blocker.Status,
			markdownCell(blocker.Reason),
		)
	}
	return out.String()
}
