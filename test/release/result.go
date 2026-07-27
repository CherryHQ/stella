// Package release defines the shared result contract used by Stella's
// release-only test runners.
package release

import (
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	// SchemaVersion is the current JSON schema version for release results.
	SchemaVersion = 1
)

// Status is the normalized outcome emitted by every release test runner.
type Status string

const (
	// StatusPass means the Scenario passed without a retry.
	StatusPass Status = "pass"
	// StatusProductFailure means Stella behavior failed and release is blocked.
	StatusProductFailure Status = "product_failure"
	// StatusExternalBlocked means an external dependency prevented a conclusion.
	StatusExternalBlocked Status = "external_blocked"
	// StatusNotRun means the Scenario was explicitly skipped with a reason.
	StatusNotRun Status = "not_run"
	// StatusFlaky means a retry succeeded after an earlier failed attempt.
	StatusFlaky Status = "flaky"
	// StatusManualPending means a required manual Scenario is not complete.
	StatusManualPending Status = "manual_pending"
	// StatusWaived means an eligible non-product outcome has an explicit waiver.
	StatusWaived Status = "waived"
)

// RunnerKind identifies the test boundary that produced a result.
type RunnerKind string

const (
	// RunnerGo covers package-level Go unit and integration tests.
	RunnerGo RunnerKind = "go"
	// RunnerSystem covers real stellad subprocess tests.
	RunnerSystem RunnerKind = "system"
	// RunnerBrowser covers Playwright browser journeys.
	RunnerBrowser RunnerKind = "browser"
	// RunnerContract covers deterministic protocol fakes.
	RunnerContract RunnerKind = "contract"
	// RunnerPackage covers candidate archive and deployment checks.
	RunnerPackage RunnerKind = "package"
	// RunnerLive covers real third-party smoke tests.
	RunnerLive RunnerKind = "live"
	// RunnerManual covers the concentrated release manual gate.
	RunnerManual RunnerKind = "manual"
)

var (
	runIDPattern       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	commitPattern      = regexp.MustCompile(`^[0-9a-fA-F]{7,64}$`)
	capabilityPattern  = regexp.MustCompile(`^[CX][0-9]{2}$`)
	scenarioPattern    = regexp.MustCompile(`^([CX][0-9]{2})-S[0-9]{2}$`)
	platformPattern    = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
	artifactKind       = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	sha256Pattern      = regexp.MustCompile(`^[0-9a-f]{64}$`)
	allowedRunnerKinds = map[RunnerKind]struct{}{
		RunnerGo: {}, RunnerSystem: {}, RunnerBrowser: {}, RunnerContract: {},
		RunnerPackage: {}, RunnerLive: {}, RunnerManual: {},
	}
	allowedStatuses = map[Status]struct{}{
		StatusPass: {}, StatusProductFailure: {}, StatusExternalBlocked: {},
		StatusNotRun: {}, StatusFlaky: {}, StatusManualPending: {},
		StatusWaived: {},
	}
	waivableStatuses = map[Status]struct{}{
		StatusExternalBlocked: {}, StatusNotRun: {}, StatusFlaky: {},
		StatusManualPending: {},
	}
)

// Run is the immutable identity shared by all results for one release
// candidate. Platform is recorded per Result because one Run spans a matrix.
type Run struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Commit  string `json:"commit"`
}

// Validate checks that a Run can safely identify one immutable candidate.
func (r Run) Validate() error {
	if !runIDPattern.MatchString(r.ID) {
		return fmt.Errorf("run.id %q must contain only letters, digits, dot, underscore, or hyphen", r.ID)
	}
	if strings.TrimSpace(r.Version) == "" || containsControl(r.Version) {
		return fmt.Errorf("run.version is required and cannot contain control characters")
	}
	if !commitPattern.MatchString(r.Commit) {
		return fmt.Errorf("run.commit %q must be a 7 to 64 character hexadecimal commit", r.Commit)
	}
	return nil
}

// Platform identifies one operating-system and architecture pair covered by a
// consolidated Scenario result.
type Platform struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
}

// Validate checks that a Platform uses stable machine-readable identifiers.
func (p Platform) Validate() error {
	if !platformPattern.MatchString(p.OS) {
		return fmt.Errorf("platform.os %q must be a lowercase machine identifier", p.OS)
	}
	if !platformPattern.MatchString(p.Arch) {
		return fmt.Errorf("platform.arch %q must be a lowercase machine identifier", p.Arch)
	}
	return nil
}

// Runner identifies the concrete runner implementation that produced a
// Scenario result.
type Runner struct {
	Kind RunnerKind `json:"kind"`
	Name string     `json:"name"`
}

// Artifact points at one diagnostic or evidence file below the current Run's
// canonical result directory.
type Artifact struct {
	Kind   string `json:"kind"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256,omitempty"`
}

// Waiver records an explicit release-owner decision for an eligible outcome.
// Product failures are intentionally absent from the allowed original states.
type Waiver struct {
	OriginalStatus Status    `json:"original_status"`
	Approver       string    `json:"approver"`
	Reason         string    `json:"reason"`
	Commit         string    `json:"commit"`
	ScenarioID     string    `json:"scenario_id"`
	ApprovedAt     time.Time `json:"approved_at"`
}

// Result is the canonical, runner-independent outcome for one Scenario
// attempt. Composite runners consolidate their platform matrix before writing
// this record.
type Result struct {
	SchemaVersion int        `json:"schema_version"`
	Run           Run        `json:"run"`
	Platforms     []Platform `json:"platforms"`
	CapabilityID  string     `json:"capability_id"`
	ScenarioID    string     `json:"scenario_id"`
	Runner        Runner     `json:"runner"`
	Attempt       int        `json:"attempt"`
	StartedAt     time.Time  `json:"started_at"`
	FinishedAt    time.Time  `json:"finished_at"`
	Status        Status     `json:"status"`
	Reason        string     `json:"reason,omitempty"`
	Artifacts     []Artifact `json:"artifacts,omitempty"`
	Waiver        *Waiver    `json:"waiver,omitempty"`
}

// Validate rejects ambiguous, stale-prone, or unsafe result records before
// they can influence a release decision.
func (r Result) Validate() error {
	if r.SchemaVersion != SchemaVersion {
		return fmt.Errorf("schema_version must be %d, got %d", SchemaVersion, r.SchemaVersion)
	}
	if err := r.Run.Validate(); err != nil {
		return err
	}
	if !capabilityPattern.MatchString(r.CapabilityID) {
		return fmt.Errorf("capability_id %q must match Cnn or Xnn", r.CapabilityID)
	}
	match := scenarioPattern.FindStringSubmatch(r.ScenarioID)
	if match == nil || match[1] != r.CapabilityID {
		return fmt.Errorf("scenario_id %q must belong to capability %s", r.ScenarioID, r.CapabilityID)
	}
	if len(r.Platforms) == 0 {
		return fmt.Errorf("scenario %s requires at least one platform", r.ScenarioID)
	}
	seenPlatforms := make(map[string]struct{}, len(r.Platforms))
	for i, platform := range r.Platforms {
		if err := platform.Validate(); err != nil {
			return fmt.Errorf("platforms[%d]: %w", i, err)
		}
		key := platform.OS + "/" + platform.Arch
		if _, exists := seenPlatforms[key]; exists {
			return fmt.Errorf("scenario %s repeats platform %s", r.ScenarioID, key)
		}
		seenPlatforms[key] = struct{}{}
	}
	if _, ok := allowedRunnerKinds[r.Runner.Kind]; !ok {
		return fmt.Errorf("runner.kind %q is not supported", r.Runner.Kind)
	}
	if strings.TrimSpace(r.Runner.Name) == "" || containsControl(r.Runner.Name) {
		return fmt.Errorf("runner.name is required and cannot contain control characters")
	}
	if r.Attempt < 1 {
		return fmt.Errorf("attempt must be at least 1")
	}
	if err := validateTimestamp("started_at", r.StartedAt); err != nil {
		return err
	}
	if err := validateTimestamp("finished_at", r.FinishedAt); err != nil {
		return err
	}
	if r.FinishedAt.Before(r.StartedAt) {
		return fmt.Errorf("finished_at cannot be before started_at")
	}
	if _, ok := allowedStatuses[r.Status]; !ok {
		return fmt.Errorf("status %q is not supported", r.Status)
	}
	if r.Status != StatusPass && strings.TrimSpace(r.Reason) == "" {
		return fmt.Errorf("status %s requires a reason", r.Status)
	}
	if containsControl(r.Reason) {
		return fmt.Errorf("reason cannot contain control characters")
	}
	if r.Status == StatusPass && r.Attempt > 1 {
		return fmt.Errorf("attempt %d cannot be pass; a retry that succeeds must be flaky", r.Attempt)
	}
	if r.Status == StatusFlaky && r.Attempt < 2 {
		return fmt.Errorf("flaky status requires attempt 2 or later")
	}
	if err := r.validateArtifacts(); err != nil {
		return err
	}
	if err := r.validateWaiver(); err != nil {
		return err
	}
	return nil
}

func (r Result) validateArtifacts() error {
	seen := make(map[string]struct{}, len(r.Artifacts))
	prefix := RunRelativeDir(r.Run.ID) + "/"
	for i, artifact := range r.Artifacts {
		if !artifactKind.MatchString(artifact.Kind) {
			return fmt.Errorf("artifacts[%d].kind %q must be kebab-case", i, artifact.Kind)
		}
		if artifact.Path == "" || strings.Contains(artifact.Path, `\`) {
			return fmt.Errorf("artifacts[%d].path must use a repository-relative slash path", i)
		}
		clean := path.Clean(artifact.Path)
		if clean != artifact.Path || !strings.HasPrefix(clean, prefix) || clean == strings.TrimSuffix(prefix, "/") {
			return fmt.Errorf("artifacts[%d].path %q must stay below %s", i, artifact.Path, strings.TrimSuffix(prefix, "/"))
		}
		if _, exists := seen[clean]; exists {
			return fmt.Errorf("artifact path %q is repeated", clean)
		}
		seen[clean] = struct{}{}
		if artifact.SHA256 != "" && !sha256Pattern.MatchString(artifact.SHA256) {
			return fmt.Errorf("artifacts[%d].sha256 must be 64 lowercase hexadecimal characters", i)
		}
	}
	return nil
}

func (r Result) validateWaiver() error {
	if r.Status != StatusWaived {
		if r.Waiver != nil {
			return fmt.Errorf("status %s cannot include a waiver", r.Status)
		}
		return nil
	}
	if r.Waiver == nil {
		return fmt.Errorf("waived status requires waiver details")
	}
	if _, ok := waivableStatuses[r.Waiver.OriginalStatus]; !ok {
		return fmt.Errorf("waiver original_status %q is not waivable", r.Waiver.OriginalStatus)
	}
	if strings.TrimSpace(r.Waiver.Approver) == "" || containsControl(r.Waiver.Approver) {
		return fmt.Errorf("waiver.approver is required and cannot contain control characters")
	}
	if strings.TrimSpace(r.Waiver.Reason) == "" || containsControl(r.Waiver.Reason) {
		return fmt.Errorf("waiver.reason is required and cannot contain control characters")
	}
	if r.Waiver.Commit != r.Run.Commit {
		return fmt.Errorf("waiver.commit must match run.commit")
	}
	if r.Waiver.ScenarioID != r.ScenarioID {
		return fmt.Errorf("waiver.scenario_id must match result scenario_id")
	}
	if err := validateTimestamp("waiver.approved_at", r.Waiver.ApprovedAt); err != nil {
		return err
	}
	if r.Waiver.ApprovedAt.Before(r.FinishedAt) {
		return fmt.Errorf("waiver.approved_at cannot be before finished_at")
	}
	return nil
}

// RunRelativeDir returns the canonical slash-separated repository path for one
// release Run's results and diagnostics.
func RunRelativeDir(runID string) string {
	return path.Join("dist", "test-results", "release", runID)
}

// RunDirectory returns the platform-native canonical directory for one Run.
func RunDirectory(repositoryRoot, runID string) string {
	return filepath.Join(repositoryRoot, filepath.FromSlash(RunRelativeDir(runID)))
}

func validateTimestamp(name string, value time.Time) error {
	if value.IsZero() {
		return fmt.Errorf("%s is required", name)
	}
	_, offset := value.Zone()
	if offset != 0 {
		return fmt.Errorf("%s must use UTC", name)
	}
	return nil
}

func containsControl(value string) bool {
	return strings.ContainsAny(value, "\r\n\t\x00")
}
