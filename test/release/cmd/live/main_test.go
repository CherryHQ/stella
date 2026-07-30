//go:build capability

package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/test/capabilities"
	livetest "github.com/CherryHQ/stella/test/live"
	"gopkg.in/yaml.v3"
)

func TestValidateRegistryCoverageRequiresEveryAutomaticLiveScenario(t *testing.T) {
	manifest := &capabilities.Manifest{
		Capabilities: []capabilities.Capability{{
			ID: "X12",
			Scenarios: []capabilities.Scenario{
				{ID: "X12-S02", Layer: "live", ReleasePolicy: "blocking"},
				{ID: "X12-S03", Layer: "live", ReleasePolicy: "manual"},
			},
		}},
	}
	registry := &livetest.Registry{
		SchemaVersion: livetest.RegistrySchemaVersion,
		Targets: []livetest.Target{{
			ID:           "provider",
			CapabilityID: "X12",
			ScenarioID:   "X12-S02",
			Adapter:      livetest.PendingAdapter,
			Summary:      "Provider target.",
		}},
	}
	if err := validateRegistryCoverage(manifest, registry); err != nil {
		t.Fatal(err)
	}

	registry.Targets = nil
	err := validateRegistryCoverage(manifest, registry)
	if err == nil || !strings.Contains(err.Error(), "requires one X12 target") {
		t.Fatalf("expected missing target error, got %v", err)
	}
}

func TestRenderResourcesContainsNamesButNoValues(t *testing.T) {
	registry := &livetest.Registry{
		SchemaVersion: livetest.RegistrySchemaVersion,
		Targets: []livetest.Target{{
			ID:           "provider",
			CapabilityID: "X12",
			ScenarioID:   "X12-S02",
			Adapter:      livetest.PendingAdapter,
			Summary:      "Provider target.",
			Resources: []livetest.Resource{{
				Name:        "STELLA_LIVE_PROVIDER_TARGETS_JSON",
				Secret:      true,
				Required:    true,
				Description: "Company provider target.",
			}},
		}},
	}
	output := renderResources(registry)
	if !strings.Contains(output, "STELLA_LIVE_PROVIDER_TARGETS_JSON") ||
		!strings.Contains(output, "Company provider target.") {
		t.Fatalf("unexpected resource output:\n%s", output)
	}
}

func TestRegisteredAdaptersIncludesImplementedCherryINTargets(t *testing.T) {
	adapters := registeredAdapters()
	for _, name := range []string{"cherryin-embedding", "cherryin-providers"} {
		if adapters[name] == nil {
			t.Errorf("registeredAdapters[%q] is missing", name)
		}
	}
	if len(adapters) != 2 {
		t.Errorf("registered adapter count = %d, want 2", len(adapters))
	}
}

func TestReleaseWorkflowWiresFullGateBeforePromotion(t *testing.T) {
	root, err := filepath.Abs("../../../..")
	if err != nil {
		t.Fatal(err)
	}
	workflowPath := filepath.Join(root, ".github", "workflows", "release.yml")
	data, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := yaml.Unmarshal(data, &document); err != nil {
		t.Fatalf("decode release workflow: %v", err)
	}
	jobs, ok := document["jobs"].(map[string]any)
	if !ok {
		t.Fatal("release workflow has no jobs mapping")
	}

	// Automatic producers must all converge before the one manual wait, and
	// Promotion must depend on the strict final gate rather than raw test jobs.
	assertWorkflowNeeds(t, jobs, "automatic-summary", []string{
		"browser-e2e",
		"candidate-index",
		"contract-tests",
		"core-results",
		"live-results",
		"platform-results",
	})
	assertWorkflowNeeds(t, jobs, "manual-gate", []string{"automatic-summary", "candidate-index"})
	assertWorkflowNeeds(t, jobs, "release-gate", []string{"candidate-index", "manual-gate"})
	assertWorkflowNeeds(t, jobs, "promotion", []string{"candidate-index", "release-gate"})
	assertWorkflowEnvironment(t, jobs, "live-results", "release-test")
	assertWorkflowEnvironment(t, jobs, "automatic-summary", "release-test")
	assertWorkflowEnvironment(t, jobs, "manual-gate", "release-approval")
	assertWorkflowEnvironment(t, jobs, "release-gate", "release-test")

	registry, err := livetest.LoadRegistry(filepath.Join(root, defaultLiveRegistryPath))
	if err != nil {
		t.Fatal(err)
	}
	liveEnv := workflowJobEnv(t, jobs, "live-results")
	summaryEnv := workflowJobEnv(t, jobs, "automatic-summary")
	gateEnv := workflowJobEnv(t, jobs, "release-gate")
	for _, target := range registry.Targets {
		for _, resource := range target.Resources {
			source := "vars"
			if resource.Secret {
				source = "secrets"
			}
			expression := "${{ " + source + "." + resource.Name + " }}"
			assertWorkflowEnvValue(t, "live-results", liveEnv, resource.Name, expression)
			if resource.Secret {
				// Both aggregate stages need the configured values to scan every
				// automatic and manual artifact for accidental disclosure.
				assertWorkflowEnvValue(t, "automatic-summary", summaryEnv, resource.Name, expression)
				assertWorkflowEnvValue(t, "release-gate", gateEnv, resource.Name, expression)
			}
		}
	}
	for _, secretName := range registry.SecretEnvNames() {
		assertWorkflowSecretList(t, "automatic-summary", summaryEnv, secretName)
		assertWorkflowSecretList(t, "release-gate", gateEnv, secretName)
	}
}

func assertWorkflowNeeds(t *testing.T, jobs map[string]any, name string, expected []string) {
	t.Helper()
	job := workflowJob(t, jobs, name)
	var actual []string
	switch value := job["needs"].(type) {
	case string:
		actual = []string{value}
	case []any:
		for _, item := range value {
			need, ok := item.(string)
			if !ok {
				t.Fatalf("job %s has non-string need %#v", name, item)
			}
			actual = append(actual, need)
		}
	default:
		t.Fatalf("job %s has invalid needs value %#v", name, job["needs"])
	}
	sort.Strings(actual)
	sort.Strings(expected)
	if strings.Join(actual, ",") != strings.Join(expected, ",") {
		t.Errorf("job %s needs %v; want %v", name, actual, expected)
	}
}

func assertWorkflowEnvironment(t *testing.T, jobs map[string]any, name, expected string) {
	t.Helper()
	job := workflowJob(t, jobs, name)
	if actual, ok := job["environment"].(string); !ok || actual != expected {
		t.Errorf("job %s environment is %#v; want %q", name, job["environment"], expected)
	}
}

func workflowJob(t *testing.T, jobs map[string]any, name string) map[string]any {
	t.Helper()
	job, ok := jobs[name].(map[string]any)
	if !ok {
		t.Fatalf("release workflow has no %s job", name)
	}
	return job
}

func workflowJobEnv(t *testing.T, jobs map[string]any, name string) map[string]any {
	t.Helper()
	job := workflowJob(t, jobs, name)
	env, ok := job["env"].(map[string]any)
	if !ok {
		t.Fatalf("job %s has no env mapping", name)
	}
	return env
}

func assertWorkflowEnvValue(t *testing.T, jobName string, env map[string]any, name, expected string) {
	t.Helper()
	if actual, ok := env[name].(string); !ok || actual != expected {
		t.Errorf("job %s env %s is %#v; want %q", jobName, name, env[name], expected)
	}
}

func assertWorkflowSecretList(t *testing.T, jobName string, env map[string]any, secretName string) {
	t.Helper()
	raw, ok := env["STELLA_RELEASE_SECRET_ENVS"].(string)
	if !ok {
		t.Fatalf("job %s has no STELLA_RELEASE_SECRET_ENVS string", jobName)
	}
	for _, name := range strings.Split(raw, ",") {
		if name == secretName {
			return
		}
	}
	t.Errorf("job %s secret scan omits %s", jobName, secretName)
}
