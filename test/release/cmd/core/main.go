//go:build capability

// Command core maps Stella's existing Go and real-process System suites into
// the shared release Result contract and records explicit uncovered gaps.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/CherryHQ/stella/test/capabilities"
	releasecontract "github.com/CherryHQ/stella/test/release"
)

const coreTimeout = 30 * time.Minute

type scenarioRef struct {
	CapabilityID string
	Scenario     capabilities.Scenario
}

type suiteGroup struct {
	Name      string
	Runner    releasecontract.Runner
	Command   []string
	Scenarios []scenarioRef
}

type corePlan struct {
	Groups  []suiteGroup
	Backlog []scenarioRef
}

// specializedScenarios are emitted by candidate, platform, Browser, Contract,
// Live, or Manual runners. Keeping this ownership explicit prevents duplicate
// attempts from making the aggregate ambiguous.
var specializedScenarios = map[string]string{
	"C02-S02": "browser",
	"C05-S02": "browser",
	"C06-S02": "browser",
	"C07-S03": "browser",
	"C17-S02": "browser",
	"X02-S02": "browser",
	"X07-S02": "browser",
	"X07-S01": "contract",
	"X09-S01": "contract",
	"X09-S02": "contract",
	"X15-S01": "contract",
	"X15-S02": "contract",
	"X19-S02": "contract",
	"X01-S02": "platform",
	"X18-S01": "platform",
	"X18-S02": "platform",
}

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
	plan, err := buildCorePlan(manifest)
	if err != nil {
		exitError(err)
	}
	run, present, err := releasecontract.RunFromEnv()
	if err != nil {
		exitError(err)
	}
	if !present {
		exitError(fmt.Errorf("core runner requires STELLA_RELEASE_* metadata"))
	}
	attempt, err := workflowAttempt()
	if err != nil {
		exitError(err)
	}
	if err := executePlan(context.Background(), root, run, attempt, plan); err != nil {
		exitError(err)
	}
}

func buildCorePlan(manifest *capabilities.Manifest) (corePlan, error) {
	if manifest == nil {
		return corePlan{}, fmt.Errorf("capability manifest is nil")
	}
	goGroup := suiteGroup{
		Name:    "go-integration",
		Runner:  releasecontract.Runner{Kind: releasecontract.RunnerGo, Name: "release-go-suite"},
		Command: []string{"mise", "run", "test"},
	}
	systemGroup := suiteGroup{
		Name:    "candidate-system",
		Runner:  releasecontract.Runner{Kind: releasecontract.RunnerSystem, Name: "release-system-suite"},
		Command: []string{"mise", "run", "system-test"},
	}
	var backlog []scenarioRef
	seenScenarios := map[string]capabilities.Scenario{}
	for _, capability := range manifest.Capabilities {
		for _, scenario := range capability.Scenarios {
			seenScenarios[scenario.ID] = scenario
			ref := scenarioRef{CapabilityID: capability.ID, Scenario: scenario}
			if scenario.Status == "missing" || scenario.Status == "partial" {
				if scenario.Layer != "live" && scenario.ReleasePolicy != "manual" {
					backlog = append(backlog, ref)
				}
				continue
			}
			if scenario.Status == "manual-only" || scenario.Layer == "live" {
				continue
			}
			if owner := specializedScenarios[scenario.ID]; owner != "" {
				continue
			}
			switch scenario.Layer {
			case "integration", "contract":
				goGroup.Scenarios = append(goGroup.Scenarios, ref)
			case "system":
				systemGroup.Scenarios = append(systemGroup.Scenarios, ref)
			default:
				return corePlan{}, fmt.Errorf(
					"covered Scenario %s at layer %s has no release runner owner",
					scenario.ID,
					scenario.Layer,
				)
			}
		}
	}
	for scenarioID, owner := range specializedScenarios {
		scenario, exists := seenScenarios[scenarioID]
		if !exists {
			continue
		}
		if scenario.Status != "covered" {
			return corePlan{}, fmt.Errorf(
				"specialized %s Scenario %s must be covered, got %s",
				owner,
				scenarioID,
				scenario.Status,
			)
		}
		if !specializedLayerMatches(owner, scenario.Layer) {
			return corePlan{}, fmt.Errorf(
				"specialized %s Scenario %s cannot own layer %s",
				owner,
				scenarioID,
				scenario.Layer,
			)
		}
	}
	sortScenarioRefs(goGroup.Scenarios)
	sortScenarioRefs(systemGroup.Scenarios)
	sortScenarioRefs(backlog)
	return corePlan{Groups: []suiteGroup{goGroup, systemGroup}, Backlog: backlog}, nil
}

func specializedLayerMatches(owner, layer string) bool {
	switch owner {
	case "browser":
		return layer == "browser"
	case "contract":
		return layer == "contract"
	case "platform":
		return layer == "package" || layer == "system"
	default:
		return false
	}
}

func executePlan(
	ctx context.Context,
	root string,
	run releasecontract.Run,
	attempt int,
	plan corePlan,
) error {
	ctx, cancel := context.WithTimeout(ctx, coreTimeout)
	defer cancel()
	var finalErr error
	for _, group := range plan.Groups {
		outcome, err := executeGroup(ctx, root, run, attempt, group)
		if err != nil {
			finalErr = errors.Join(finalErr, err)
		}
		for _, scenario := range group.Scenarios {
			result := resultFromGroup(run, attempt, scenario, group.Runner, outcome)
			if err := writeResult(root, result); err != nil {
				finalErr = errors.Join(finalErr, err)
			}
		}
	}
	for _, scenario := range plan.Backlog {
		result := backlogResult(run, attempt, scenario, time.Now().UTC())
		if err := writeResult(root, result); err != nil {
			finalErr = errors.Join(finalErr, err)
		}
	}
	return finalErr
}

type groupOutcome struct {
	StartedAt  time.Time
	FinishedAt time.Time
	Status     releasecontract.Status
	Reason     string
	Artifact   releasecontract.Artifact
}

func executeGroup(
	ctx context.Context,
	root string,
	run releasecontract.Run,
	attempt int,
	group suiteGroup,
) (groupOutcome, error) {
	startedAt := time.Now().UTC()
	logDir := filepath.Join(releasecontract.RunDirectory(root, run.ID), "artifacts", "core")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return groupOutcome{}, fmt.Errorf("create core log directory: %w", err)
	}
	logPath := filepath.Join(logDir, fmt.Sprintf("%s-a%03d.log", group.Name, attempt))
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return groupOutcome{}, fmt.Errorf("create core log: %w", err)
	}
	command := exec.CommandContext(ctx, group.Command[0], group.Command[1:]...)
	command.Dir = root
	command.Env = os.Environ()
	command.Stdout = io.MultiWriter(os.Stdout, logFile)
	command.Stderr = io.MultiWriter(os.Stderr, logFile)
	runErr := command.Run()
	closeErr := logFile.Close()
	runErr = errors.Join(runErr, closeErr)
	finishedAt := time.Now().UTC()

	status := releasecontract.StatusPass
	reason := ""
	if runErr != nil {
		status = releasecontract.StatusProductFailure
		reason = oneLine(runErr.Error())
	}
	if status == releasecontract.StatusPass && attempt > 1 {
		status = releasecontract.StatusFlaky
		reason = fmt.Sprintf("release workflow attempt %d passed after a retry", attempt)
	}
	artifact, artifactErr := releasecontract.ArtifactForFile(root, run.ID, "core-suite-log", logPath)
	if artifactErr != nil {
		return groupOutcome{}, artifactErr
	}
	outcome := groupOutcome{
		StartedAt:  startedAt,
		FinishedAt: finishedAt,
		Status:     status,
		Reason:     reason,
		Artifact:   artifact,
	}
	if runErr != nil {
		return outcome, fmt.Errorf("%s failed; see %s: %w", group.Name, logPath, runErr)
	}
	return outcome, nil
}

func resultFromGroup(
	run releasecontract.Run,
	attempt int,
	scenario scenarioRef,
	runner releasecontract.Runner,
	outcome groupOutcome,
) releasecontract.Result {
	return releasecontract.Result{
		SchemaVersion: releasecontract.SchemaVersion,
		Run:           run,
		Platforms:     []releasecontract.Platform{{OS: "linux", Arch: "amd64"}},
		CapabilityID:  scenario.CapabilityID,
		ScenarioID:    scenario.Scenario.ID,
		Runner:        runner,
		Attempt:       attempt,
		StartedAt:     outcome.StartedAt,
		FinishedAt:    outcome.FinishedAt,
		Status:        outcome.Status,
		Reason:        outcome.Reason,
		Artifacts:     []releasecontract.Artifact{outcome.Artifact},
	}
}

func backlogResult(
	run releasecontract.Run,
	attempt int,
	scenario scenarioRef,
	now time.Time,
) releasecontract.Result {
	reason := oneLine(scenario.Scenario.Gap)
	if reason == "" {
		reason = "capability inventory records this Scenario as " + scenario.Scenario.Status
	}
	return releasecontract.Result{
		SchemaVersion: releasecontract.SchemaVersion,
		Run:           run,
		Platforms:     []releasecontract.Platform{{OS: "linux", Arch: "amd64"}},
		CapabilityID:  scenario.CapabilityID,
		ScenarioID:    scenario.Scenario.ID,
		Runner: releasecontract.Runner{
			Kind: runnerKindForLayer(scenario.Scenario.Layer),
			Name: "release-coverage-backlog",
		},
		Attempt:    attempt,
		StartedAt:  now,
		FinishedAt: now,
		Status:     releasecontract.StatusNotRun,
		Reason:     reason,
	}
}

func writeResult(root string, result releasecontract.Result) error {
	if err := result.Validate(); err != nil {
		return fmt.Errorf("validate %s core result: %w", result.ScenarioID, err)
	}
	if _, err := releasecontract.WriteResult(releasecontract.RunDirectory(root, result.Run.ID), result); err != nil {
		return fmt.Errorf("write %s core result: %w", result.ScenarioID, err)
	}
	return releasecontract.ValidateArtifactFiles(root, result)
}

func runnerKindForLayer(layer string) releasecontract.RunnerKind {
	switch layer {
	case "browser":
		return releasecontract.RunnerBrowser
	case "contract":
		return releasecontract.RunnerContract
	case "package":
		return releasecontract.RunnerPackage
	case "system":
		return releasecontract.RunnerSystem
	default:
		return releasecontract.RunnerGo
	}
}

func workflowAttempt() (int, error) {
	raw := os.Getenv("GITHUB_RUN_ATTEMPT")
	if raw == "" {
		return 1, nil
	}
	attempt, err := strconv.Atoi(raw)
	if err != nil || attempt < 1 {
		return 0, fmt.Errorf("GITHUB_RUN_ATTEMPT must be a positive integer")
	}
	return attempt, nil
}

func sortScenarioRefs(refs []scenarioRef) {
	sort.Slice(refs, func(i, j int) bool { return refs[i].Scenario.ID < refs[j].Scenario.ID })
}

func oneLine(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	const maxReasonLength = 500
	if len(value) > maxReasonLength {
		return value[:maxReasonLength] + "..."
	}
	return value
}

func exitError(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
