//go:build capability

// Command live validates the non-secret target registry, prints its resource
// contract, or executes every automatic Live Scenario for one release Run.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/CherryHQ/stella/test/capabilities"
	livetest "github.com/CherryHQ/stella/test/live"
	releasecontract "github.com/CherryHQ/stella/test/release"
)

const defaultLiveRegistryPath = "test/live/targets.yaml"

func main() {
	mode := flag.String("mode", "run", "run, validate, or resources")
	rootFlag := flag.String("root", ".", "repository root")
	registryPath := flag.String("registry", defaultLiveRegistryPath, "live target registry path relative to root")
	manifestPath := flag.String("manifest", "test/capabilities.yaml", "capability manifest path relative to root")
	flag.Parse()

	root, err := filepath.Abs(*rootFlag)
	if err != nil {
		exitError(fmt.Errorf("resolve repository root: %w", err))
	}
	registry, err := livetest.LoadRegistry(filepath.Join(root, *registryPath))
	if err != nil {
		exitError(err)
	}
	manifest, err := capabilities.LoadManifest(filepath.Join(root, *manifestPath))
	if err != nil {
		exitError(err)
	}
	if err := validateRegistryCoverage(manifest, registry); err != nil {
		exitError(err)
	}

	switch *mode {
	case "validate":
		fmt.Printf("live target registry valid: %d automatic targets\n", len(registry.Targets))
	case "resources":
		fmt.Print(renderResources(registry))
	case "run":
		if err := runLive(context.Background(), root, registry); err != nil {
			exitError(err)
		}
	default:
		exitError(fmt.Errorf("unsupported live mode %q", *mode))
	}
}

func runLive(ctx context.Context, root string, registry *livetest.Registry) error {
	run, present, err := releasecontract.RunFromEnv()
	if err != nil {
		return err
	}
	if !present {
		return fmt.Errorf("live runner requires STELLA_RELEASE_* metadata")
	}
	attempt, err := workflowAttempt()
	if err != nil {
		return err
	}
	executor := livetest.Executor{
		Adapters:        registeredAdapters(),
		LookupEnv:       os.LookupEnv,
		Now:             time.Now,
		WorkflowAttempt: attempt,
		CandidateBinary: os.Getenv("STELLA_SYSTEM_BINARY"),
	}
	executions, err := executor.Execute(ctx, run, registry)
	if err != nil {
		return err
	}
	secrets := presentRegistrySecrets(registry)
	runDir := releasecontract.RunDirectory(root, run.ID)
	var productFailures []string
	for i := range executions {
		if err := writeExecution(root, runDir, secrets, &executions[i]); err != nil {
			return err
		}
		result := executions[i].Result
		fmt.Printf("%s %s: %s\n", result.ScenarioID, executions[i].TargetID, result.Status)
		if result.Status == releasecontract.StatusProductFailure {
			productFailures = append(productFailures, result.ScenarioID)
		}
	}
	if len(productFailures) > 0 {
		sort.Strings(productFailures)
		return fmt.Errorf("live Product Failure: %s", strings.Join(productFailures, ", "))
	}
	return nil
}

// registeredAdapters contains only implemented real-target drivers. Unknown or
// pending adapters still emit Not Run instead of silently passing.
func registeredAdapters() map[string]livetest.Adapter {
	return map[string]livetest.Adapter{
		"cherryin-embedding": livetest.NewCherryINEmbeddingAdapter(),
		"cherryin-providers": livetest.NewCherryINProviderAdapter(),
	}
}

func writeExecution(
	root string,
	runDir string,
	secrets map[string]string,
	execution *livetest.Execution,
) error {
	data, err := json.MarshalIndent(execution, "", "  ")
	if err != nil {
		return fmt.Errorf("encode live execution: %w", err)
	}
	data = append(data, '\n')
	if err := releasecontract.CheckBytesForSecrets("live execution", data, secrets); err != nil {
		return err
	}
	artifactDir := filepath.Join(runDir, "artifacts", "live")
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		return fmt.Errorf("create live artifact directory: %w", err)
	}
	artifactPath := filepath.Join(
		artifactDir,
		fmt.Sprintf("%s-a%03d.json", execution.TargetID, execution.Result.Attempt),
	)
	file, err := os.OpenFile(artifactPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("create live execution artifact: %w", err)
	}
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return fmt.Errorf("write live execution artifact: %w", err)
	}
	artifact, err := releasecontract.ArtifactForFile(root, execution.Result.Run.ID, "live-execution", artifactPath)
	if err != nil {
		return err
	}
	execution.Result.Artifacts = []releasecontract.Artifact{artifact}
	if err := execution.Result.Validate(); err != nil {
		return fmt.Errorf("validate %s live result: %w", execution.Result.ScenarioID, err)
	}
	if _, err := releasecontract.WriteResult(runDir, execution.Result); err != nil {
		return fmt.Errorf("write %s live result: %w", execution.Result.ScenarioID, err)
	}
	return releasecontract.ValidateArtifactFiles(root, execution.Result)
}

func presentRegistrySecrets(registry *livetest.Registry) map[string]string {
	secrets := map[string]string{}
	for _, name := range registry.SecretEnvNames() {
		if value, present := os.LookupEnv(name); present && value != "" {
			secrets[name] = value
		}
	}
	return secrets
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

func validateRegistryCoverage(manifest *capabilities.Manifest, registry *livetest.Registry) error {
	expected := map[string]string{}
	for _, capability := range manifest.Capabilities {
		for _, scenario := range capability.Scenarios {
			if scenario.Layer == "live" && scenario.ReleasePolicy != "manual" {
				expected[scenario.ID] = capability.ID
			}
		}
	}
	actual := map[string]string{}
	for _, target := range registry.Targets {
		if _, exists := actual[target.ScenarioID]; exists {
			return fmt.Errorf("live Scenario %s has more than one target", target.ScenarioID)
		}
		actual[target.ScenarioID] = target.CapabilityID
	}
	for scenarioID, capabilityID := range expected {
		if actual[scenarioID] != capabilityID {
			return fmt.Errorf("automatic Live Scenario %s requires one %s target", scenarioID, capabilityID)
		}
	}
	for scenarioID := range actual {
		if _, exists := expected[scenarioID]; !exists {
			return fmt.Errorf("live registry target %s is not an automatic Live Scenario", scenarioID)
		}
	}
	return nil
}

func renderResources(registry *livetest.Registry) string {
	var out strings.Builder
	fmt.Fprintln(&out, "# Release Live target resources")
	fmt.Fprintln(&out)
	fmt.Fprintln(&out, "| Scenario | Target | Adapter | Environment variable | Secret | Required | Description |")
	fmt.Fprintln(&out, "| --- | --- | --- | --- | --- | --- | --- |")
	for _, target := range registry.Targets {
		if len(target.Resources) == 0 {
			fmt.Fprintf(
				&out,
				"| %s | %s | %s | - | false | false | No external input declared. |\n",
				target.ScenarioID,
				target.ID,
				target.Adapter,
			)
			continue
		}
		for _, resource := range target.Resources {
			fmt.Fprintf(
				&out,
				"| %s | %s | %s | `%s` | %t | %t | %s |\n",
				target.ScenarioID,
				target.ID,
				target.Adapter,
				resource.Name,
				resource.Secret,
				resource.Required,
				strings.ReplaceAll(resource.Description, "|", "\\|"),
			)
		}
	}
	return out.String()
}

func exitError(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
