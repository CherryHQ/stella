//go:build capability

package capabilities

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const currentSchemaVersion = 2

var (
	capabilityIDPattern = regexp.MustCompile(`^[CX][0-9]{2}$`)
	assetIDPattern      = regexp.MustCompile(`^A[0-9]{2}$`)
	namePattern         = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
)

var allowedLayers = map[string]struct{}{
	"unit": {}, "integration": {}, "system": {}, "browser": {},
	"contract": {}, "live": {}, "package": {}, "manual": {},
}

var allowedStatuses = map[string]struct{}{
	"covered": {}, "partial": {}, "missing": {}, "manual-only": {},
}

var allowedReleasePolicies = map[string]struct{}{
	"blocking": {}, "nonblocking": {}, "manual": {},
}

var allowedAssetCategories = map[string]struct{}{
	"framework": {}, "helper": {}, "harness": {}, "orchestration": {},
	"quality-gate": {}, "execution": {}, "exploratory": {},
}

var allowedEvidenceKinds = map[string]struct{}{
	"go_test": {}, "system_test": {}, "contract_test": {},
	"runbook": {}, "task": {}, "workflow": {},
}

// ValidationError contains every independently actionable inventory problem.
type ValidationError struct {
	Problems []string
}

// Error implements error with deterministic ordering for stable local output.
func (e *ValidationError) Error() string {
	problems := append([]string(nil), e.Problems...)
	sort.Strings(problems)
	return "capability inventory validation failed:\n- " + strings.Join(problems, "\n- ")
}

// Validate checks the manifest structure, evidence references, and every
// repository surface available to the caller. CLI comparison is performed when
// actual.CLICommands is non-nil.
func Validate(root string, manifest *Manifest, actual RepositorySurfaces) error {
	collector := &problemCollector{}
	collector.validateManifest(root, manifest)
	collector.compareAllSurfaces(manifest, actual)
	return collector.err()
}

// ValidateCLICommands compares the manifest to the command tree built by
// cmd/stellad.newApp(). It is separate because Go package main is not importable.
func ValidateCLICommands(manifest *Manifest, actual []string) error {
	collector := &problemCollector{}
	collector.compareSurface(
		"cli_commands",
		manifest.DeclaredSurfaces().CLICommands,
		manifest.SurfaceExemptions.CLICommands,
		actual,
	)
	return collector.err()
}

type problemCollector struct {
	problems []string
}

func (c *problemCollector) add(format string, args ...any) {
	c.problems = append(c.problems, fmt.Sprintf(format, args...))
}

func (c *problemCollector) err() error {
	if len(c.problems) == 0 {
		return nil
	}
	return &ValidationError{Problems: c.problems}
}

func (c *problemCollector) validateManifest(root string, manifest *Manifest) {
	if manifest == nil {
		c.add("manifest is nil")
		return
	}
	if manifest.SchemaVersion != currentSchemaVersion {
		c.add("schema_version must be %d, got %d", currentSchemaVersion, manifest.SchemaVersion)
	}
	if strings.TrimSpace(manifest.Baseline.Commit) == "" {
		c.add("baseline.commit is required")
	}

	capabilityIDs := map[string]struct{}{}
	capabilityNames := map[string]struct{}{}
	scenarioIDs := map[string]struct{}{}
	for i := range manifest.Capabilities {
		capability := &manifest.Capabilities[i]
		location := fmt.Sprintf("capabilities[%d]", i)
		if !capabilityIDPattern.MatchString(capability.ID) {
			c.add("%s.id %q must match Cnn or Xnn", location, capability.ID)
		}
		if _, exists := capabilityIDs[capability.ID]; exists {
			c.add("duplicate capability id %q", capability.ID)
		}
		capabilityIDs[capability.ID] = struct{}{}
		if capability.Class != "core" && capability.Class != "extension" {
			c.add("capability %s has unknown class %q", capability.ID, capability.Class)
		}
		if !namePattern.MatchString(capability.Name) {
			c.add("capability %s name %q must be kebab-case", capability.ID, capability.Name)
		}
		if _, exists := capabilityNames[capability.Name]; exists {
			c.add("duplicate capability name %q", capability.Name)
		}
		capabilityNames[capability.Name] = struct{}{}
		if strings.TrimSpace(capability.Title) == "" || strings.TrimSpace(capability.Description) == "" {
			c.add("capability %s requires title and description", capability.ID)
		}
		if len(capability.Scenarios) == 0 {
			c.add("capability %s has no scenarios", capability.ID)
		}
		for j := range capability.Scenarios {
			scenario := &capability.Scenarios[j]
			c.validateScenario(root, capability.ID, scenario)
			if _, exists := scenarioIDs[scenario.ID]; exists {
				c.add("duplicate scenario id %q", scenario.ID)
			}
			scenarioIDs[scenario.ID] = struct{}{}
		}
	}

	assetIDs := map[string]struct{}{}
	for i := range manifest.TestAssets {
		asset := &manifest.TestAssets[i]
		if !assetIDPattern.MatchString(asset.ID) {
			c.add("test_assets[%d].id %q must match Ann", i, asset.ID)
		}
		if _, exists := assetIDs[asset.ID]; exists {
			c.add("duplicate test asset id %q", asset.ID)
		}
		assetIDs[asset.ID] = struct{}{}
		if _, ok := allowedAssetCategories[asset.Category]; !ok {
			c.add("test asset %s has unknown category %q", asset.ID, asset.Category)
		}
		if strings.TrimSpace(asset.Name) == "" || strings.TrimSpace(asset.Summary) == "" {
			c.add("test asset %s requires name and summary", asset.ID)
		}
		for _, path := range asset.EvidencePaths {
			c.validateRelativePath(root, fmt.Sprintf("test asset %s", asset.ID), path)
		}
	}
}

func (c *problemCollector) validateScenario(root, capabilityID string, scenario *Scenario) {
	if !regexp.MustCompile(`^` + regexp.QuoteMeta(capabilityID) + `-S[0-9]{2}$`).MatchString(scenario.ID) {
		c.add("scenario id %q must match %s-Snn", scenario.ID, capabilityID)
	}
	if !namePattern.MatchString(scenario.Name) {
		c.add("scenario %s name %q must be kebab-case", scenario.ID, scenario.Name)
	}
	if _, ok := allowedLayers[scenario.Layer]; !ok {
		c.add("scenario %s has unknown layer %q", scenario.ID, scenario.Layer)
	}
	if _, ok := allowedStatuses[scenario.Status]; !ok {
		c.add("scenario %s has unknown status %q", scenario.ID, scenario.Status)
	}
	if strings.TrimSpace(scenario.ReleasePolicy) == "" {
		c.add("scenario %s requires release_policy", scenario.ID)
	} else if _, ok := allowedReleasePolicies[scenario.ReleasePolicy]; !ok {
		c.add("scenario %s has unknown release_policy %q", scenario.ID, scenario.ReleasePolicy)
	}
	if scenario.ReleasePolicy == "manual" && scenario.Status != "manual-only" {
		c.add("scenario %s with release_policy manual must have status manual-only", scenario.ID)
	}
	if scenario.Layer == "live" && scenario.ReleasePolicy == "blocking" {
		c.add("live scenario %s cannot be blocking; deterministic self-hosted targets belong in contract or package layers", scenario.ID)
	}
	if strings.TrimSpace(scenario.Summary) == "" {
		c.add("scenario %s requires a summary", scenario.ID)
	}
	if scenario.Status != "covered" && strings.TrimSpace(scenario.Gap) == "" {
		c.add("scenario %s with status %s requires a gap", scenario.ID, scenario.Status)
	}

	hasDirectAutomated := false
	hasRunbook := false
	for i := range scenario.Evidence {
		evidence := &scenario.Evidence[i]
		location := fmt.Sprintf("scenario %s evidence[%d]", scenario.ID, i)
		if _, ok := allowedEvidenceKinds[evidence.Kind]; !ok {
			c.add("%s has unknown kind %q", location, evidence.Kind)
		}
		if strings.TrimSpace(evidence.Proves) == "" {
			c.add("%s requires proves", location)
		}
		c.validateRelativePath(root, location, evidence.Path)
		if evidence.Kind == "go_test" || evidence.Kind == "system_test" || evidence.Kind == "contract_test" {
			c.validateGoTestReference(root, location, evidence)
			if evidence.Direct {
				hasDirectAutomated = true
			}
		}
		if evidence.Kind == "system_test" && evidence.Test == "TestSystem" && evidence.Subtest != "" && !evidence.Direct {
			c.add("%s TestSystem journey must be direct evidence", location)
		}
		if evidence.Kind == "runbook" {
			hasRunbook = true
			if evidence.Direct {
				c.add("%s runbook cannot be direct automated evidence", location)
			}
		}
	}
	if scenario.Status == "covered" && !hasDirectAutomated {
		c.add("covered scenario %s requires direct automated evidence", scenario.ID)
	}
	if scenario.Status == "manual-only" && !hasRunbook {
		c.add("manual-only scenario %s requires runbook evidence", scenario.ID)
	}
}

func (c *problemCollector) validateRelativePath(root, location, path string) {
	if path == "" {
		c.add("%s requires a path", location)
		return
	}
	clean := filepath.Clean(path)
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		c.add("%s path %q must stay inside the repository", location, path)
		return
	}
	if _, err := os.Stat(filepath.Join(root, clean)); err != nil {
		c.add("%s path %q does not exist", location, path)
	}
}

func (c *problemCollector) validateGoTestReference(root, location string, evidence *Evidence) {
	if !strings.HasSuffix(evidence.Path, "_test.go") {
		c.add("%s path %q must be a Go test file", location, evidence.Path)
		return
	}
	if evidence.Test == "" {
		return
	}
	path := filepath.Join(root, filepath.Clean(evidence.Path))
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		c.add("%s cannot parse %q: %v", location, evidence.Path, err)
		return
	}
	testName := strings.SplitN(evidence.Test, "/", 2)[0]
	found := false
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if ok && function.Name.Name == testName {
			found = true
			break
		}
	}
	if !found {
		c.add("%s test %q does not exist in %q", location, evidence.Test, evidence.Path)
	}
	if evidence.Subtest != "" {
		data, err := os.ReadFile(path)
		if err == nil && !strings.Contains(string(data), strconv.Quote(evidence.Subtest)) {
			c.add("%s subtest %q is not a string literal in %q", location, evidence.Subtest, evidence.Path)
		}
	}
}

func (c *problemCollector) compareAllSurfaces(manifest *Manifest, actual RepositorySurfaces) {
	if manifest == nil {
		return
	}
	declared := manifest.DeclaredSurfaces()
	c.compareSurface("openapi", declared.OpenAPI, manifest.SurfaceExemptions.OpenAPI, actual.OpenAPI)
	c.compareSurface("web_routes", declared.WebRoutes, manifest.SurfaceExemptions.WebRoutes, actual.WebRoutes)
	if actual.CLICommands != nil {
		c.compareSurface("cli_commands", declared.CLICommands, manifest.SurfaceExemptions.CLICommands, actual.CLICommands)
	}
	c.compareSurface("plugins", declared.Plugins, manifest.SurfaceExemptions.Plugins, actual.Plugins)
	c.compareSurface("system_skills", declared.SystemSkills, manifest.SurfaceExemptions.SystemSkills, actual.SystemSkills)
	c.compareSurface("builtin_souls", declared.BuiltinSouls, manifest.SurfaceExemptions.BuiltinSouls, actual.BuiltinSouls)
	c.compareSurface("builtin_delegates", declared.BuiltinDelegates, manifest.SurfaceExemptions.BuiltinDelegates, actual.BuiltinDelegates)
	c.compareSurface("builtin_templates", declared.BuiltinTemplates, manifest.SurfaceExemptions.BuiltinTemplates, actual.BuiltinTemplates)
	c.compareSurface("core_tools", declared.CoreTools, manifest.SurfaceExemptions.CoreTools, actual.CoreTools)
	c.compareEvidenceCatalog("system_journeys", manifest.DeclaredSystemJourneys(), actual.SystemJourneys)
}

// compareEvidenceCatalog requires every discovered executable journey to have
// scenario evidence and rejects references to deleted journeys. One journey may
// directly support more than one scenario, so duplicate references are valid.
func (c *problemCollector) compareEvidenceCatalog(kind string, declared, actual []string) {
	declaredSet := make(map[string]struct{}, len(declared))
	for _, id := range declared {
		if id == "" {
			c.add("%s contains an empty evidence reference", kind)
			continue
		}
		declaredSet[id] = struct{}{}
	}
	actualSet := make(map[string]struct{}, len(actual))
	for _, id := range actual {
		actualSet[id] = struct{}{}
		if _, mapped := declaredSet[id]; !mapped {
			c.add("%s evidence %q is not mapped", kind, id)
		}
	}
	for id := range declaredSet {
		if _, exists := actualSet[id]; !exists {
			c.add("%s evidence %q no longer exists", kind, id)
		}
	}
}

func (c *problemCollector) compareSurface(kind string, declared []string, exemptions []Exemption, actual []string) {
	owners := make(map[string]struct{}, len(declared))
	for _, id := range declared {
		if id == "" {
			c.add("%s contains an empty mapped surface", kind)
			continue
		}
		if _, exists := owners[id]; exists {
			c.add("%s surface %q is mapped more than once", kind, id)
		}
		owners[id] = struct{}{}
	}
	exempt := make(map[string]struct{}, len(exemptions))
	for _, item := range exemptions {
		if item.ID == "" || strings.TrimSpace(item.Reason) == "" {
			c.add("%s exemption %q requires id and reason", kind, item.ID)
		}
		if _, mapped := owners[item.ID]; mapped {
			c.add("%s surface %q cannot be both mapped and exempt", kind, item.ID)
		}
		if _, exists := exempt[item.ID]; exists {
			c.add("%s surface %q is exempt more than once", kind, item.ID)
		}
		exempt[item.ID] = struct{}{}
	}

	actualSet := make(map[string]struct{}, len(actual))
	for _, id := range actual {
		actualSet[id] = struct{}{}
		_, mapped := owners[id]
		_, ignored := exempt[id]
		if !mapped && !ignored {
			c.add("%s surface %q is not mapped or exempt", kind, id)
		}
	}
	for id := range owners {
		if _, exists := actualSet[id]; !exists {
			c.add("%s mapped surface %q no longer exists", kind, id)
		}
	}
	for id := range exempt {
		if _, exists := actualSet[id]; !exists {
			c.add("%s exempt surface %q no longer exists", kind, id)
		}
	}
}
