// Package live defines the non-secret target registry and execution boundary
// for release-only checks against real third-party systems.
package live

import (
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	// RegistrySchemaVersion is the current targets.yaml contract version.
	RegistrySchemaVersion = 1
	// PendingAdapter records a target whose real adapter is intentionally not
	// implemented until the corresponding company-owned resource is provided.
	PendingAdapter = "pending"
)

var (
	targetIDPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	envNamePattern  = regexp.MustCompile(`^STELLA_LIVE_[A-Z0-9_]+$`)
)

// Registry lists every automatic Live Scenario and its non-secret resource
// contract. Secret values are supplied only through the named environment
// variables at runtime.
type Registry struct {
	SchemaVersion int      `yaml:"schema_version" json:"schema_version"`
	Targets       []Target `yaml:"targets" json:"targets"`
}

// Target binds one capability Scenario to a concrete adapter and its required
// release-test resources.
type Target struct {
	ID           string     `yaml:"id" json:"id"`
	CapabilityID string     `yaml:"capability_id" json:"capability_id"`
	ScenarioID   string     `yaml:"scenario_id" json:"scenario_id"`
	Adapter      string     `yaml:"adapter" json:"adapter"`
	Summary      string     `yaml:"summary" json:"summary"`
	Resources    []Resource `yaml:"resources" json:"resources"`
}

// Resource describes one environment-provided target input without storing its
// value. Secret resources are included in the artifact leak scan.
type Resource struct {
	Name        string `yaml:"name" json:"name"`
	Secret      bool   `yaml:"secret" json:"secret"`
	Required    bool   `yaml:"required" json:"required"`
	Description string `yaml:"description" json:"description"`
}

// LoadRegistry decodes a registry and rejects unknown keys or trailing YAML.
func LoadRegistry(path string) (*Registry, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open live target registry: %w", err)
	}
	defer func() { _ = file.Close() }()

	decoder := yaml.NewDecoder(file)
	decoder.KnownFields(true)
	var registry Registry
	if err := decoder.Decode(&registry); err != nil {
		return nil, fmt.Errorf("decode live target registry: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, fmt.Errorf("decode live target registry: multiple YAML documents are not allowed")
		}
		return nil, fmt.Errorf("decode live target registry trailing content: %w", err)
	}
	if err := registry.Validate(); err != nil {
		return nil, err
	}
	return &registry, nil
}

// Validate rejects ambiguous targets and unsafe environment-variable names.
func (r Registry) Validate() error {
	if r.SchemaVersion != RegistrySchemaVersion {
		return fmt.Errorf("live registry schema_version must be %d", RegistrySchemaVersion)
	}
	if len(r.Targets) == 0 {
		return fmt.Errorf("live registry requires at least one target")
	}
	targetIDs := make(map[string]struct{}, len(r.Targets))
	scenarioIDs := make(map[string]struct{}, len(r.Targets))
	for i, target := range r.Targets {
		if !targetIDPattern.MatchString(target.ID) {
			return fmt.Errorf("targets[%d].id %q must be kebab-case", i, target.ID)
		}
		if _, exists := targetIDs[target.ID]; exists {
			return fmt.Errorf("live target id %s is repeated", target.ID)
		}
		targetIDs[target.ID] = struct{}{}
		if !regexp.MustCompile(`^X[0-9]{2}$`).MatchString(target.CapabilityID) {
			return fmt.Errorf("target %s capability_id %q must match Xnn", target.ID, target.CapabilityID)
		}
		if !regexp.MustCompile(`^X[0-9]{2}-S[0-9]{2}$`).MatchString(target.ScenarioID) ||
			!strings.HasPrefix(target.ScenarioID, target.CapabilityID+"-") {
			return fmt.Errorf("target %s scenario_id %q must belong to %s", target.ID, target.ScenarioID, target.CapabilityID)
		}
		if _, exists := scenarioIDs[target.ScenarioID]; exists {
			return fmt.Errorf("live Scenario %s has more than one target", target.ScenarioID)
		}
		scenarioIDs[target.ScenarioID] = struct{}{}
		if !targetIDPattern.MatchString(target.Adapter) {
			return fmt.Errorf("target %s adapter %q must be kebab-case", target.ID, target.Adapter)
		}
		if strings.TrimSpace(target.Summary) == "" || containsControl(target.Summary) {
			return fmt.Errorf("target %s summary is required and cannot contain control characters", target.ID)
		}
		resourceNames := make(map[string]struct{}, len(target.Resources))
		for j, resource := range target.Resources {
			if !envNamePattern.MatchString(resource.Name) {
				return fmt.Errorf(
					"target %s resources[%d].name %q must use the STELLA_LIVE_* namespace",
					target.ID,
					j,
					resource.Name,
				)
			}
			if _, exists := resourceNames[resource.Name]; exists {
				return fmt.Errorf("target %s repeats resource %s", target.ID, resource.Name)
			}
			resourceNames[resource.Name] = struct{}{}
			if strings.TrimSpace(resource.Description) == "" || containsControl(resource.Description) {
				return fmt.Errorf("target %s resource %s requires a single-line description", target.ID, resource.Name)
			}
		}
	}
	return nil
}

// SecretEnvNames returns the stable, sorted set of registry-owned secret names.
func (r Registry) SecretEnvNames() []string {
	seen := map[string]struct{}{}
	for _, target := range r.Targets {
		for _, resource := range target.Resources {
			if resource.Secret {
				seen[resource.Name] = struct{}{}
			}
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ResourceEnvNames returns every declared input name in stable order.
func (t Target) ResourceEnvNames() []string {
	names := make([]string, 0, len(t.Resources))
	for _, resource := range t.Resources {
		names = append(names, resource.Name)
	}
	sort.Strings(names)
	return names
}

func containsControl(value string) bool {
	return strings.ContainsAny(value, "\r\n\t\x00")
}
