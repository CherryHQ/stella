//go:build capability

// Command manual records the concentrated release-owner decisions and applies
// explicit waivers to eligible automatic outcomes for the same candidate.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/CherryHQ/stella/test/capabilities"
	manualtest "github.com/CherryHQ/stella/test/manual"
	releasecontract "github.com/CherryHQ/stella/test/release"
)

const (
	envManualCommit = "STELLA_MANUAL_COMMIT"
	envApprover     = "STELLA_MANUAL_APPROVER"
	envWaiversJSON  = "STELLA_RELEASE_WAIVERS_JSON"
)

func main() {
	rootFlag := flag.String("root", ".", "repository root")
	manifestPath := flag.String("manifest", "test/capabilities.yaml", "capability manifest path relative to root")
	flag.Parse()

	root, err := filepath.Abs(*rootFlag)
	if err != nil {
		exitError(fmt.Errorf("resolve repository root: %w", err))
	}
	manifest, err := capabilities.LoadManifest(filepath.Join(root, *manifestPath))
	if err != nil {
		exitError(err)
	}
	run, present, err := releasecontract.RunFromEnv()
	if err != nil {
		exitError(err)
	}
	if !present {
		exitError(fmt.Errorf("manual runner requires STELLA_RELEASE_* metadata"))
	}
	if err := runManual(root, run, manifest, os.LookupEnv, time.Now().UTC()); err != nil {
		exitError(err)
	}
}

func runManual(
	root string,
	run releasecontract.Run,
	manifest *capabilities.Manifest,
	lookup func(string) (string, bool),
	now time.Time,
) error {
	runDir := releasecontract.RunDirectory(root, run.ID)
	results, err := releasecontract.LoadResults(filepath.Join(runDir, "results"))
	if err != nil {
		return err
	}
	attempts := maximumAttempts(results)
	manualScenarios, manualIDs := manifestManualScenarios(manifest)
	commit, _ := lookup(envManualCommit)
	approver, _ := lookup(envApprover)
	commitMatches := commit == run.Commit
	var finalErr error
	if !commitMatches {
		finalErr = errors.Join(finalErr, fmt.Errorf("%s must match the candidate commit", envManualCommit))
	}

	for _, scenario := range manualScenarios {
		decision := manualtest.ReadDecision(scenario.ScenarioID, lookup)
		if !commitMatches {
			decision = manualtest.Decision{}
		}
		attempt := attempts[scenario.ScenarioID] + 1
		result, evidence, err := manualtest.BuildManualResult(run, scenario, decision, approver, attempt, now)
		if err != nil {
			finalErr = errors.Join(finalErr, err)
			// Invalid reviewer input remains pending rather than disappearing
			// from the aggregate report.
			result, evidence, err = manualtest.BuildManualResult(
				run,
				scenario,
				manualtest.Decision{},
				"",
				attempt,
				now,
			)
			if err != nil {
				return errors.Join(finalErr, err)
			}
			result.Reason = "manual decision input is invalid; correct the release-approval Environment variables"
			evidence.Reason = result.Reason
		}
		if !commitMatches {
			result.Reason = envManualCommit + " is missing or does not match the current candidate"
			evidence.Reason = result.Reason
		}
		if err := writeDecision(root, result, evidence); err != nil {
			finalErr = errors.Join(finalErr, err)
		} else {
			attempts[scenario.ScenarioID] = attempt
		}
		if result.Status == releasecontract.StatusProductFailure {
			finalErr = errors.Join(finalErr, fmt.Errorf("%s is a Product Failure", scenario.ScenarioID))
		}
	}

	rawWaivers, _ := lookup(envWaiversJSON)
	requests, err := manualtest.ParseWaiverRequests(rawWaivers)
	if err != nil {
		finalErr = errors.Join(finalErr, err)
		return finalErr
	}
	if len(requests) > 0 && !commitMatches {
		return errors.Join(finalErr, fmt.Errorf("waivers require %s to match the candidate", envManualCommit))
	}
	for _, request := range requests {
		if manualIDs[request.ScenarioID] {
			finalErr = errors.Join(finalErr, fmt.Errorf(
				"manual Scenario %s must use its dedicated STATUS variables, not %s",
				request.ScenarioID,
				envWaiversJSON,
			))
			continue
		}
		original, err := currentWaivableResult(results, request.ScenarioID)
		if err != nil {
			finalErr = errors.Join(finalErr, err)
			continue
		}
		attempt := attempts[request.ScenarioID] + 1
		result, evidence, err := manualtest.BuildWaiverResult(
			original,
			request,
			approver,
			attempt,
			now,
		)
		if err != nil {
			finalErr = errors.Join(finalErr, err)
			continue
		}
		if err := writeDecision(root, result, evidence); err != nil {
			finalErr = errors.Join(finalErr, err)
			continue
		}
		attempts[request.ScenarioID] = attempt
	}
	return finalErr
}

func manifestManualScenarios(manifest *capabilities.Manifest) ([]manualtest.Scenario, map[string]bool) {
	var scenarios []manualtest.Scenario
	ids := map[string]bool{}
	for _, capability := range manifest.Capabilities {
		for _, scenario := range capability.Scenarios {
			if scenario.ReleasePolicy != "manual" {
				continue
			}
			scenarios = append(scenarios, manualtest.Scenario{
				CapabilityID: capability.ID,
				ScenarioID:   scenario.ID,
			})
			ids[scenario.ID] = true
		}
	}
	sort.Slice(scenarios, func(i, j int) bool { return scenarios[i].ScenarioID < scenarios[j].ScenarioID })
	return scenarios, ids
}

func maximumAttempts(results []releasecontract.Result) map[string]int {
	attempts := map[string]int{}
	for _, result := range results {
		if result.Attempt > attempts[result.ScenarioID] {
			attempts[result.ScenarioID] = result.Attempt
		}
	}
	return attempts
}

func currentWaivableResult(results []releasecontract.Result, scenarioID string) (releasecontract.Result, error) {
	var current *releasecontract.Result
	for i := range results {
		result := &results[i]
		if result.ScenarioID != scenarioID {
			continue
		}
		if result.Status == releasecontract.StatusProductFailure {
			return releasecontract.Result{}, fmt.Errorf("Scenario %s has Product Failure and cannot be waived", scenarioID)
		}
		if result.Status == releasecontract.StatusWaived {
			continue
		}
		if current == nil || result.Attempt > current.Attempt {
			current = result
		}
	}
	if current == nil {
		return releasecontract.Result{}, fmt.Errorf("Scenario %s has no automatic result to waive", scenarioID)
	}
	switch current.Status {
	case releasecontract.StatusExternalBlocked,
		releasecontract.StatusNotRun,
		releasecontract.StatusFlaky,
		releasecontract.StatusManualPending:
		return *current, nil
	default:
		return releasecontract.Result{}, fmt.Errorf(
			"Scenario %s current status %s is not waivable",
			scenarioID,
			current.Status,
		)
	}
}

func writeDecision(
	root string,
	result releasecontract.Result,
	evidence manualtest.EvidenceRecord,
) error {
	data, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s manual evidence: %w", result.ScenarioID, err)
	}
	data = append(data, '\n')
	runDir := releasecontract.RunDirectory(root, result.Run.ID)
	artifactDir := filepath.Join(runDir, "artifacts", "manual")
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		return fmt.Errorf("create manual artifact directory: %w", err)
	}
	artifactPath := filepath.Join(
		artifactDir,
		fmt.Sprintf("%s-a%03d.json", strings.ToLower(result.ScenarioID), result.Attempt),
	)
	file, err := os.OpenFile(artifactPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("create manual evidence: %w", err)
	}
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return fmt.Errorf("write manual evidence: %w", err)
	}
	artifact, err := releasecontract.ArtifactForFile(root, result.Run.ID, "manual-evidence", artifactPath)
	if err != nil {
		return err
	}
	result.Artifacts = []releasecontract.Artifact{artifact}
	if err := result.Validate(); err != nil {
		return fmt.Errorf("validate %s manual result: %w", result.ScenarioID, err)
	}
	if _, err := releasecontract.WriteResult(runDir, result); err != nil {
		return fmt.Errorf("write %s manual result: %w", result.ScenarioID, err)
	}
	return releasecontract.ValidateArtifactFiles(root, result)
}

func exitError(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
