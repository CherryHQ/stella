//go:build capability

// Package capabilities loads and validates Stella's release capability inventory.
package capabilities

import (
	"fmt"
	"io"
	"os"

	"gopkg.in/yaml.v3"
)

// Manifest is the versioned source of truth for release capabilities, surfaces,
// existing test assets, and current scenario evidence.
type Manifest struct {
	SchemaVersion     int               `yaml:"schema_version" json:"schema_version"`
	Baseline          Baseline          `yaml:"baseline" json:"baseline"`
	SurfaceExemptions SurfaceExemptions `yaml:"surface_exemptions" json:"surface_exemptions"`
	TestAssets        []TestAsset       `yaml:"test_assets" json:"test_assets"`
	Capabilities      []Capability      `yaml:"capabilities" json:"capabilities"`
}

// Baseline identifies the source revision used for the initial inventory.
type Baseline struct {
	Commit string `yaml:"commit" json:"commit"`
	Note   string `yaml:"note" json:"note"`
}

// SurfaceRefs lists concrete product surfaces owned by one capability.
type SurfaceRefs struct {
	OpenAPI      []string `yaml:"openapi" json:"openapi"`
	WebRoutes    []string `yaml:"web_routes" json:"web_routes"`
	CLICommands  []string `yaml:"cli_commands" json:"cli_commands"`
	Plugins      []string `yaml:"plugins" json:"plugins"`
	SystemSkills []string `yaml:"system_skills" json:"system_skills"`
}

// SurfaceExemptions records discovered surfaces that are intentionally not a
// product capability, together with a reviewable reason.
type SurfaceExemptions struct {
	OpenAPI      []Exemption `yaml:"openapi" json:"openapi"`
	WebRoutes    []Exemption `yaml:"web_routes" json:"web_routes"`
	CLICommands  []Exemption `yaml:"cli_commands" json:"cli_commands"`
	Plugins      []Exemption `yaml:"plugins" json:"plugins"`
	SystemSkills []Exemption `yaml:"system_skills" json:"system_skills"`
}

// Exemption explains why a discovered surface is not mapped to a capability.
type Exemption struct {
	ID     string `yaml:"id" json:"id"`
	Reason string `yaml:"reason" json:"reason"`
}

// TestAsset describes an existing framework, helper, runner, quality gate, or
// exploratory tool without claiming that every asset proves product behavior.
type TestAsset struct {
	ID            string   `yaml:"id" json:"id"`
	Category      string   `yaml:"category" json:"category"`
	Name          string   `yaml:"name" json:"name"`
	Automation    string   `yaml:"automation" json:"automation"`
	Entrypoints   []string `yaml:"entrypoints" json:"entrypoints"`
	EvidencePaths []string `yaml:"evidence_paths" json:"evidence_paths"`
	Summary       string   `yaml:"summary" json:"summary"`
	Limitations   string   `yaml:"limitations" json:"limitations"`
}

// Capability groups one stable product behavior across all of its entrypoints.
type Capability struct {
	ID          string      `yaml:"id" json:"id"`
	Class       string      `yaml:"class" json:"class"`
	Name        string      `yaml:"name" json:"name"`
	Title       string      `yaml:"title" json:"title"`
	Description string      `yaml:"description" json:"description"`
	Surfaces    SurfaceRefs `yaml:"surfaces" json:"surfaces"`
	Scenarios   []Scenario  `yaml:"scenarios" json:"scenarios"`
}

// Scenario is one observable release acceptance behavior and its current
// evidence status.
type Scenario struct {
	ID            string     `yaml:"id" json:"id"`
	Name          string     `yaml:"name" json:"name"`
	Layer         string     `yaml:"layer" json:"layer"`
	Status        string     `yaml:"status" json:"status"`
	ReleasePolicy string     `yaml:"release_policy" json:"release_policy"`
	Summary       string     `yaml:"summary" json:"summary"`
	Evidence      []Evidence `yaml:"evidence" json:"evidence"`
	Gap           string     `yaml:"gap" json:"gap"`
}

// Evidence points at an existing, parseable test or runbook reference.
type Evidence struct {
	Kind    string `yaml:"kind" json:"kind"`
	Path    string `yaml:"path" json:"path"`
	Test    string `yaml:"test" json:"test"`
	Subtest string `yaml:"subtest" json:"subtest"`
	Direct  bool   `yaml:"direct" json:"direct"`
	Proves  string `yaml:"proves" json:"proves"`
}

// LoadManifest decodes a manifest and rejects unknown fields so misspelled
// inventory keys cannot silently disable validation.
func LoadManifest(path string) (*Manifest, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open capability manifest: %w", err)
	}
	defer f.Close()

	decoder := yaml.NewDecoder(f)
	decoder.KnownFields(true)
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return nil, fmt.Errorf("decode capability manifest: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("decode capability manifest: multiple YAML documents are not allowed")
		}
		return nil, fmt.Errorf("decode capability manifest trailing content: %w", err)
	}
	return &manifest, nil
}

// DeclaredSurfaces returns every surface listed by capabilities. Duplicate
// ownership is checked separately by Validate.
func (m *Manifest) DeclaredSurfaces() SurfaceRefs {
	var refs SurfaceRefs
	for _, capability := range m.Capabilities {
		refs.OpenAPI = append(refs.OpenAPI, capability.Surfaces.OpenAPI...)
		refs.WebRoutes = append(refs.WebRoutes, capability.Surfaces.WebRoutes...)
		refs.CLICommands = append(refs.CLICommands, capability.Surfaces.CLICommands...)
		refs.Plugins = append(refs.Plugins, capability.Surfaces.Plugins...)
		refs.SystemSkills = append(refs.SystemSkills, capability.Surfaces.SystemSkills...)
	}
	return refs
}
