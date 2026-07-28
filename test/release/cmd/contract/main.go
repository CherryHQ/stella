//go:build linux

// Command contract runs Stella's deterministic self-hosted protocol contracts
// and emits one shared release Result per covered Scenario.
package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	releasecontract "github.com/CherryHQ/stella/test/release"
)

const contractCommandTimeout = 15 * time.Minute

type scenarioRef struct {
	CapabilityID string
	ScenarioID   string
}

type contractCommand struct {
	Args []string
	Env  []string
}

type contractGroup struct {
	Name      string
	Scenarios []scenarioRef
	Commands  []contractCommand
}

var contractGroups = []contractGroup{
	{
		Name:      "webhook-ingress",
		Scenarios: []scenarioRef{{CapabilityID: "X07", ScenarioID: "X07-S01"}},
		Commands: []contractCommand{
			{Args: []string{"go", "test", "-count=1", "./plugins/channels/webhook"}},
			{Args: []string{
				"go", "test", "-count=1", "./internal/server", "-run",
				"^(TestWebhookIngressGates|TestDrainWebhookStream|TestDrainWebhookStreamPreservesBusy|TestPeekWebhookResult|TestWebhookLimiter|TestWebhookLimiterInflight)$",
			}},
		},
	},
	{
		Name: "mcp-streamable-http",
		Scenarios: []scenarioRef{
			{CapabilityID: "X09", ScenarioID: "X09-S01"},
			{CapabilityID: "X09", ScenarioID: "X09-S02"},
		},
		Commands: []contractCommand{
			{Args: []string{"go", "test", "-count=1", "./internal/mcp"}},
			{Args: []string{
				"go", "test", "-count=1", "./internal/server", "-run",
				"^TestMCPServerAPILifecycleAndUserScope$",
			}},
		},
	},
	{
		Name: "sandbox-backends",
		Scenarios: []scenarioRef{
			{CapabilityID: "X15", ScenarioID: "X15-S01"},
			{CapabilityID: "X15", ScenarioID: "X15-S02"},
		},
		Commands: []contractCommand{
			{
				Args: []string{
					"go", "test", "-count=1", "-timeout=10m",
					"./plugins/sandbox/local",
					"./plugins/sandbox/none",
					"./plugins/sandbox/docker/...",
				},
				Env: []string{"STELLA_REQUIRE_DOCKER_CONTRACT=1"},
			},
		},
	},
	{
		Name: "otlp-export",
		Scenarios: []scenarioRef{
			{CapabilityID: "X19", ScenarioID: "X19-S02"},
		},
		Commands: []contractCommand{
			{Args: []string{
				"go", "test", "-count=1",
				"./internal/observability/...",
				"./test/release",
			}},
		},
	},
}

type groupOutcome struct {
	Group      contractGroup
	StartedAt  time.Time
	FinishedAt time.Time
	Status     releasecontract.Status
	Reason     string
	LogPath    string
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "release contract test failed: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	rootFlag := flag.String("root", "", "absolute repository root (defaults to auto-detection)")
	flag.Parse()

	root, err := repositoryRoot(*rootFlag)
	if err != nil {
		return err
	}
	if err := validateContractGroups(contractGroups); err != nil {
		return err
	}
	runIdentity, err := contractRunIdentity(root)
	if err != nil {
		return err
	}
	attempt, err := contractAttempt()
	if err != nil {
		return err
	}

	runDir := releasecontract.RunDirectory(root, runIdentity.ID)
	artifactRoot := filepath.Join(runDir, "artifacts", "contracts")
	if err := os.MkdirAll(artifactRoot, 0o755); err != nil {
		return fmt.Errorf("create contract artifact directory: %w", err)
	}

	var runErr error
	for _, group := range contractGroups {
		outcome := executeContractGroup(root, artifactRoot, attempt, group)
		if err := writeContractResults(root, runIdentity, attempt, outcome); err != nil {
			runErr = errors.Join(runErr, err)
		}
		if outcome.Status != releasecontract.StatusPass {
			runErr = errors.Join(
				runErr,
				fmt.Errorf("%s finished with status %s", group.Name, outcome.Status),
			)
		}
	}
	if runErr != nil {
		return runErr
	}
	fmt.Printf(
		"release contract test passed: %s (%d Scenario groups)\n",
		runIdentity.ID,
		len(contractGroups),
	)
	return nil
}

func executeContractGroup(
	root string,
	artifactRoot string,
	attempt int,
	group contractGroup,
) groupOutcome {
	startedAt := time.Now().UTC()
	logPath := filepath.Join(
		artifactRoot,
		fmt.Sprintf("%s-a%03d.log", group.Name, attempt),
	)
	outcome := groupOutcome{
		Group:      group,
		StartedAt:  startedAt,
		FinishedAt: startedAt,
		Status:     releasecontract.StatusProductFailure,
		LogPath:    logPath,
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		outcome.Reason = oneLine(fmt.Sprintf("create contract log: %v", err))
		outcome.FinishedAt = time.Now().UTC()
		return outcome
	}

	var commandErr error
	for index, definition := range group.Commands {
		if len(definition.Args) == 0 {
			commandErr = fmt.Errorf("contract command %d has no arguments", index+1)
			break
		}
		_, _ = fmt.Fprintf(logFile, "$ %s\n", strings.Join(definition.Args, " "))
		ctx, cancel := context.WithTimeout(context.Background(), contractCommandTimeout)
		command := exec.CommandContext(ctx, definition.Args[0], definition.Args[1:]...)
		command.Dir = root
		command.Env = append(os.Environ(), definition.Env...)
		command.Stdout = io.MultiWriter(os.Stdout, logFile)
		command.Stderr = io.MultiWriter(os.Stderr, logFile)
		err := command.Run()
		cancel()
		if err == nil {
			continue
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			commandErr = fmt.Errorf(
				"command %d exceeded %s",
				index+1,
				contractCommandTimeout,
			)
		} else {
			commandErr = fmt.Errorf("command %d failed: %w", index+1, err)
		}
		break
	}
	closeErr := logFile.Close()
	outcome.FinishedAt = time.Now().UTC()
	if commandErr != nil || closeErr != nil {
		outcome.Reason = oneLine(errors.Join(commandErr, closeErr).Error())
		return outcome
	}
	if attempt > 1 {
		outcome.Status = releasecontract.StatusFlaky
		outcome.Reason = fmt.Sprintf(
			"release workflow attempt %d passed after a retry",
			attempt,
		)
		return outcome
	}
	outcome.Status = releasecontract.StatusPass
	return outcome
}

func writeContractResults(
	root string,
	runIdentity releasecontract.Run,
	attempt int,
	outcome groupOutcome,
) error {
	artifact, err := contractLogArtifact(root, runIdentity.ID, outcome.LogPath)
	status := outcome.Status
	reason := outcome.Reason
	var artifacts []releasecontract.Artifact
	var writeErr error
	if err != nil {
		// Result emission is more important than retaining a diagnostic. Record
		// the artifact failure as Product Failure so aggregation never mistakes a
		// missing log for a missing or successful Scenario.
		status = releasecontract.StatusProductFailure
		reason = oneLine(errors.Join(errors.New(reason), err).Error())
		writeErr = err
	} else {
		artifacts = []releasecontract.Artifact{artifact}
	}
	runDir := releasecontract.RunDirectory(root, runIdentity.ID)
	for _, scenario := range outcome.Group.Scenarios {
		result := releasecontract.Result{
			SchemaVersion: releasecontract.SchemaVersion,
			Run:           runIdentity,
			Platforms: []releasecontract.Platform{{
				OS: runtime.GOOS, Arch: runtime.GOARCH,
			}},
			CapabilityID: scenario.CapabilityID,
			ScenarioID:   scenario.ScenarioID,
			Runner: releasecontract.Runner{
				Kind: releasecontract.RunnerContract,
				Name: "go-deterministic-contract",
			},
			Attempt:    attempt,
			StartedAt:  outcome.StartedAt.UTC(),
			FinishedAt: outcome.FinishedAt.UTC(),
			Status:     status,
			Reason:     oneLine(reason),
			Artifacts:  artifacts,
		}
		if _, err := releasecontract.WriteResult(runDir, result); err != nil {
			writeErr = errors.Join(
				writeErr,
				fmt.Errorf("write %s result: %w", scenario.ScenarioID, err),
			)
			continue
		}
		if err := releasecontract.ValidateArtifactFiles(root, result); err != nil {
			writeErr = errors.Join(
				writeErr,
				fmt.Errorf("validate %s artifacts: %w", scenario.ScenarioID, err),
			)
		}
	}
	return writeErr
}

func contractLogArtifact(
	root string,
	runID string,
	logPath string,
) (releasecontract.Artifact, error) {
	data, err := os.ReadFile(logPath)
	if err != nil {
		return releasecontract.Artifact{}, fmt.Errorf("read contract log: %w", err)
	}
	relative, err := filepath.Rel(root, logPath)
	if err != nil {
		return releasecontract.Artifact{}, fmt.Errorf("resolve contract log path: %w", err)
	}
	requiredPrefix := releasecontract.RunRelativeDir(runID) + "/artifacts/contracts/"
	slashPath := filepath.ToSlash(relative)
	if !strings.HasPrefix(slashPath, requiredPrefix) {
		return releasecontract.Artifact{}, fmt.Errorf(
			"contract log %s must stay below %s",
			slashPath,
			strings.TrimSuffix(requiredPrefix, "/"),
		)
	}
	digest := sha256.Sum256(data)
	return releasecontract.Artifact{
		Kind:   "runner-log",
		Path:   slashPath,
		SHA256: hex.EncodeToString(digest[:]),
	}, nil
}

func validateContractGroups(groups []contractGroup) error {
	seenGroups := make(map[string]struct{}, len(groups))
	seenScenarios := make(map[string]struct{})
	for _, group := range groups {
		if strings.TrimSpace(group.Name) == "" {
			return fmt.Errorf("contract group name is required")
		}
		if _, exists := seenGroups[group.Name]; exists {
			return fmt.Errorf("contract group %s is repeated", group.Name)
		}
		seenGroups[group.Name] = struct{}{}
		if len(group.Commands) == 0 || len(group.Scenarios) == 0 {
			return fmt.Errorf("contract group %s requires commands and Scenarios", group.Name)
		}
		for _, scenario := range group.Scenarios {
			if _, exists := seenScenarios[scenario.ScenarioID]; exists {
				return fmt.Errorf("contract Scenario %s is repeated", scenario.ScenarioID)
			}
			seenScenarios[scenario.ScenarioID] = struct{}{}
		}
	}
	return nil
}

func repositoryRoot(explicit string) (string, error) {
	if explicit != "" {
		return filepath.Abs(explicit)
	}
	current, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolve working directory: %w", err)
	}
	for {
		if info, statErr := os.Stat(filepath.Join(current, "go.mod")); statErr == nil && !info.IsDir() {
			return current, nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("repository root not found from working directory")
		}
		current = parent
	}
}

func contractRunIdentity(root string) (releasecontract.Run, error) {
	runIdentity, present, err := releasecontract.RunFromEnv()
	if err != nil || present {
		return runIdentity, err
	}
	commitBytes, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil {
		return releasecontract.Run{}, fmt.Errorf("resolve local candidate commit: %w", err)
	}
	randomBytes := make([]byte, 4)
	if _, err := rand.Read(randomBytes); err != nil {
		return releasecontract.Run{}, fmt.Errorf("generate local Run suffix: %w", err)
	}
	runIdentity = releasecontract.Run{
		ID: "contract-" + time.Now().UTC().Format("20060102T150405") +
			"-" + hex.EncodeToString(randomBytes),
		Version: "contract-local",
		Commit:  strings.TrimSpace(string(commitBytes)),
	}
	if err := runIdentity.Validate(); err != nil {
		return releasecontract.Run{}, err
	}
	return runIdentity, nil
}

func contractAttempt() (int, error) {
	raw := strings.TrimSpace(os.Getenv("GITHUB_RUN_ATTEMPT"))
	if raw == "" {
		return 1, nil
	}
	attempt, err := strconv.Atoi(raw)
	if err != nil || attempt < 1 {
		return 0, fmt.Errorf("GITHUB_RUN_ATTEMPT must be a positive integer")
	}
	return attempt, nil
}

func oneLine(value string) string {
	return strings.Join(strings.Fields(value), " ")
}
