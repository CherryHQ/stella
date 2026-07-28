package live

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadRegistryAcceptsStrictTargetContract(t *testing.T) {
	path := filepath.Join(t.TempDir(), "targets.yaml")
	data := []byte(`
schema_version: 1
targets:
  - id: provider-smoke
    capability_id: X12
    scenario_id: X12-S02
    adapter: pending
    summary: Provider target awaiting company credentials.
    resources:
      - name: STELLA_LIVE_PROVIDER_TARGETS_JSON
        secret: true
        required: true
        description: Provider endpoint, model, API key, and budget.
`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	registry, err := LoadRegistry(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(registry.Targets) != 1 || registry.Targets[0].ScenarioID != "X12-S02" {
		t.Fatalf("unexpected registry: %+v", registry)
	}
	if names := registry.SecretEnvNames(); len(names) != 1 || names[0] != "STELLA_LIVE_PROVIDER_TARGETS_JSON" {
		t.Fatalf("unexpected secret names: %v", names)
	}
}

func TestLoadRegistryRejectsUnknownAndDuplicateFields(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want string
	}{
		{
			name: "unknown key",
			yaml: `
schema_version: 1
unknown: true
targets: []
`,
			want: "field unknown not found",
		},
		{
			name: "duplicate Scenario",
			yaml: `
schema_version: 1
targets:
  - id: first
    capability_id: X12
    scenario_id: X12-S02
    adapter: pending
    summary: First target.
    resources: []
  - id: second
    capability_id: X12
    scenario_id: X12-S02
    adapter: pending
    summary: Second target.
    resources: []
`,
			want: "has more than one target",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "targets.yaml")
			if err := os.WriteFile(path, []byte(test.yaml), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := LoadRegistry(path)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected %q, got %v", test.want, err)
			}
		})
	}
}
