package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	releasecontract "github.com/CherryHQ/stella/test/release"
)

const stageSchemaVersion = 1

var stageNamePattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

type stageRecord struct {
	SchemaVersion int                        `json:"schema_version"`
	Run           releasecontract.Run        `json:"run"`
	Platform      releasecontract.Platform   `json:"platform"`
	Name          string                     `json:"name"`
	Attempt       int                        `json:"attempt"`
	StartedAt     time.Time                  `json:"started_at"`
	FinishedAt    time.Time                  `json:"finished_at"`
	Status        releasecontract.Status     `json:"status"`
	Reason        string                     `json:"reason,omitempty"`
	Artifacts     []releasecontract.Artifact `json:"artifacts,omitempty"`
}

func (s stageRecord) validate() error {
	if s.SchemaVersion != stageSchemaVersion {
		return fmt.Errorf("stage schema_version must be %d", stageSchemaVersion)
	}
	if err := s.Run.Validate(); err != nil {
		return err
	}
	if err := s.Platform.Validate(); err != nil {
		return err
	}
	if !stageNamePattern.MatchString(s.Name) {
		return fmt.Errorf("stage name %q must be kebab-case", s.Name)
	}
	if s.Attempt < 1 {
		return fmt.Errorf("stage attempt must be at least 1")
	}
	if s.StartedAt.IsZero() || s.FinishedAt.IsZero() ||
		s.StartedAt.Location() != time.UTC || s.FinishedAt.Location() != time.UTC {
		return fmt.Errorf("stage timestamps must use UTC")
	}
	if s.FinishedAt.Before(s.StartedAt) {
		return fmt.Errorf("stage finished_at cannot be before started_at")
	}
	if s.Status != releasecontract.StatusPass && s.Status != releasecontract.StatusProductFailure {
		return fmt.Errorf("stage status %q is not supported", s.Status)
	}
	if s.Status != releasecontract.StatusPass && strings.TrimSpace(s.Reason) == "" {
		return fmt.Errorf("failed stage requires a reason")
	}
	return nil
}

func recordStage(
	root string,
	run releasecontract.Run,
	platform releasecontract.Platform,
	name string,
	attempt int,
	startedAt time.Time,
	execute func(io.Writer) error,
) error {
	if !stageNamePattern.MatchString(name) {
		return fmt.Errorf("stage name %q must be kebab-case", name)
	}
	stageDir := filepath.Join(releasecontract.RunDirectory(root, run.ID), "artifacts", "platform", "stages")
	logDir := filepath.Join(releasecontract.RunDirectory(root, run.ID), "artifacts", "platform", "logs")
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		return fmt.Errorf("create platform stage directory: %w", err)
	}
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return fmt.Errorf("create platform log directory: %w", err)
	}
	base := stageBase(platform, name, attempt)
	logPath := filepath.Join(logDir, base+".log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("create stage log: %w", err)
	}
	runErr := execute(logFile)
	closeErr := logFile.Close()
	if closeErr != nil {
		runErr = errors.Join(runErr, fmt.Errorf("close stage log: %w", closeErr))
	}

	finishedAt := time.Now().UTC()
	status := releasecontract.StatusPass
	reason := ""
	if runErr != nil {
		status = releasecontract.StatusProductFailure
		reason = oneLine(runErr.Error())
	}
	logArtifact, artifactErr := artifactForPath(root, run.ID, "stage-log", logPath)
	if artifactErr != nil {
		runErr = errors.Join(runErr, artifactErr)
		status = releasecontract.StatusProductFailure
		reason = oneLine(runErr.Error())
	}
	record := stageRecord{
		SchemaVersion: stageSchemaVersion,
		Run:           run,
		Platform:      platform,
		Name:          name,
		Attempt:       attempt,
		StartedAt:     startedAt.UTC(),
		FinishedAt:    finishedAt,
		Status:        status,
		Reason:        reason,
		Artifacts:     []releasecontract.Artifact{logArtifact},
	}
	if err := record.validate(); err != nil {
		return errors.Join(runErr, err)
	}
	recordPath := filepath.Join(stageDir, base+".json")
	if err := writeExclusiveJSON(recordPath, record); err != nil {
		return errors.Join(runErr, err)
	}
	fmt.Printf("%s %s stage %s: %s\n", platform.OS, platform.Arch, name, status)
	if runErr != nil {
		return fmt.Errorf("%s %s stage %s failed; see %s: %w", platform.OS, platform.Arch, name, logPath, runErr)
	}
	return nil
}

func assertStages(
	root string,
	run releasecontract.Run,
	platform releasecontract.Platform,
	attempt int,
	expected []string,
) error {
	records, err := loadStageRecords(root, run)
	if err != nil {
		return err
	}
	index := indexStages(records)
	var failures []string
	for _, name := range expected {
		record, ok := index[stageKey(platform, name, attempt)]
		switch {
		case !ok:
			failures = append(failures, name+" missing")
		case record.Status != releasecontract.StatusPass:
			failures = append(failures, name+" "+string(record.Status))
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("%s/%s platform stages failed: %s", platform.OS, platform.Arch, strings.Join(failures, ", "))
	}
	fmt.Printf("%s/%s platform stages passed: %s\n", platform.OS, platform.Arch, strings.Join(expected, ", "))
	return nil
}

func finalizePlatformResults(root string, run releasecontract.Run, attempt int, now time.Time) error {
	records, err := loadStageRecords(root, run)
	if err != nil {
		return err
	}
	latest := latestStages(records)
	expected := []stageExpectation{
		{Platform: releasecontract.Platform{OS: "linux", Arch: "amd64"}, Name: "binary-system"},
		{Platform: releasecontract.Platform{OS: "linux", Arch: "amd64"}, Name: "systemd"},
		{Platform: releasecontract.Platform{OS: "linux", Arch: "amd64"}, Name: "docker"},
		{Platform: releasecontract.Platform{OS: "linux", Arch: "amd64"}, Name: "helm"},
		{Platform: releasecontract.Platform{OS: "linux", Arch: "arm64"}, Name: "binary-system"},
		{Platform: releasecontract.Platform{OS: "linux", Arch: "arm64"}, Name: "docker"},
	}

	selected := make([]stageRecord, 0, len(expected))
	var missing []string
	for _, want := range expected {
		record, ok := latest[latestStageKey(want.Platform, want.Name)]
		if !ok {
			missing = append(missing, want.Platform.OS+"/"+want.Platform.Arch+":"+want.Name)
			continue
		}
		selected = append(selected, record)
	}
	deploymentResult, err := resultFromStages(
		root,
		run,
		attempt,
		now,
		"X18",
		"X18-S02",
		releasecontract.Runner{Kind: releasecontract.RunnerPackage, Name: "release-platform"},
		[]releasecontract.Platform{{OS: "linux", Arch: "amd64"}, {OS: "linux", Arch: "arm64"}},
		selected,
		missing,
	)
	if err != nil {
		return err
	}

	systemdRecord, hasSystemd := latest[latestStageKey(
		releasecontract.Platform{OS: "linux", Arch: "amd64"},
		"systemd",
	)]
	var systemdStages []stageRecord
	var systemdMissing []string
	if hasSystemd {
		systemdStages = []stageRecord{systemdRecord}
	} else {
		systemdMissing = []string{"linux/amd64:systemd"}
	}
	systemdResult, err := resultFromStages(
		root,
		run,
		attempt,
		now,
		"X01",
		"X01-S02",
		releasecontract.Runner{Kind: releasecontract.RunnerSystem, Name: "release-systemd"},
		[]releasecontract.Platform{{OS: "linux", Arch: "amd64"}},
		systemdStages,
		systemdMissing,
	)
	if err != nil {
		return err
	}

	runDir := releasecontract.RunDirectory(root, run.ID)
	if _, err := releasecontract.WriteResult(runDir, systemdResult); err != nil {
		return err
	}
	if _, err := releasecontract.WriteResult(runDir, deploymentResult); err != nil {
		return err
	}
	if err := releasecontract.ValidateArtifactFiles(root, systemdResult); err != nil {
		return err
	}
	if err := releasecontract.ValidateArtifactFiles(root, deploymentResult); err != nil {
		return err
	}

	if systemdResult.Status != releasecontract.StatusPass ||
		deploymentResult.Status != releasecontract.StatusPass {
		return fmt.Errorf(
			"platform release gate did not pass: X01-S02=%s X18-S02=%s",
			systemdResult.Status,
			deploymentResult.Status,
		)
	}
	fmt.Println("platform release gate passed: X01-S02 and X18-S02")
	return nil
}

type stageExpectation struct {
	Platform releasecontract.Platform
	Name     string
}

func resultFromStages(
	root string,
	run releasecontract.Run,
	attempt int,
	now time.Time,
	capabilityID string,
	scenarioID string,
	runner releasecontract.Runner,
	platforms []releasecontract.Platform,
	stages []stageRecord,
	missing []string,
) (releasecontract.Result, error) {
	startedAt := now.UTC()
	finishedAt := startedAt
	status := releasecontract.StatusPass
	var reasons []string
	artifactByPath := map[string]releasecontract.Artifact{}
	for _, stage := range stages {
		if stage.StartedAt.Before(startedAt) {
			startedAt = stage.StartedAt
		}
		if stage.FinishedAt.After(finishedAt) {
			finishedAt = stage.FinishedAt
		}
		if stage.Status != releasecontract.StatusPass {
			status = releasecontract.StatusProductFailure
			reasons = append(reasons, stage.Platform.OS+"/"+stage.Platform.Arch+":"+stage.Name+" failed")
		}
		for _, artifact := range stage.Artifacts {
			artifactByPath[artifact.Path] = artifact
		}
		stagePath := stageRecordPath(root, run.ID, stage)
		artifact, err := artifactForPath(root, run.ID, "stage-result", stagePath)
		if err != nil {
			return releasecontract.Result{}, err
		}
		artifactByPath[artifact.Path] = artifact
	}
	if len(missing) > 0 {
		status = releasecontract.StatusProductFailure
		reasons = append(reasons, "missing "+strings.Join(missing, ", "))
	}
	if status == releasecontract.StatusPass && attempt > 1 {
		status = releasecontract.StatusFlaky
		reasons = append(reasons, fmt.Sprintf("release workflow attempt %d passed after a retry", attempt))
	}
	artifacts := make([]releasecontract.Artifact, 0, len(artifactByPath))
	for _, artifact := range artifactByPath {
		artifacts = append(artifacts, artifact)
	}
	sort.Slice(artifacts, func(i, j int) bool { return artifacts[i].Path < artifacts[j].Path })

	result := releasecontract.Result{
		SchemaVersion: releasecontract.SchemaVersion,
		Run:           run,
		Platforms:     platforms,
		CapabilityID:  capabilityID,
		ScenarioID:    scenarioID,
		Runner:        runner,
		Attempt:       attempt,
		StartedAt:     startedAt,
		FinishedAt:    finishedAt,
		Status:        status,
		Reason:        strings.Join(reasons, "; "),
		Artifacts:     artifacts,
	}
	if err := result.Validate(); err != nil {
		return releasecontract.Result{}, err
	}
	return result, nil
}

func stageBase(platform releasecontract.Platform, name string, attempt int) string {
	return fmt.Sprintf("%s-%s-%s-a%03d", platform.OS, platform.Arch, name, attempt)
}

func stageKey(platform releasecontract.Platform, name string, attempt int) string {
	return fmt.Sprintf("%s/%s:%s:%d", platform.OS, platform.Arch, name, attempt)
}

func latestStageKey(platform releasecontract.Platform, name string) string {
	return fmt.Sprintf("%s/%s:%s", platform.OS, platform.Arch, name)
}

func indexStages(records []stageRecord) map[string]stageRecord {
	index := make(map[string]stageRecord, len(records))
	for _, record := range records {
		index[stageKey(record.Platform, record.Name, record.Attempt)] = record
	}
	return index
}

func latestStages(records []stageRecord) map[string]stageRecord {
	index := make(map[string]stageRecord, len(records))
	for _, record := range records {
		key := latestStageKey(record.Platform, record.Name)
		if previous, ok := index[key]; !ok || record.Attempt > previous.Attempt {
			index[key] = record
		}
	}
	return index
}

func loadStageRecords(root string, run releasecontract.Run) ([]stageRecord, error) {
	dir := filepath.Join(releasecontract.RunDirectory(root, run.ID), "artifacts", "platform", "stages")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read platform stages: %w", err)
	}
	var records []stageRecord
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			return nil, fmt.Errorf("unexpected platform stage entry %s", entry.Name())
		}
		path := filepath.Join(dir, entry.Name())
		record, err := loadStageRecord(path)
		if err != nil {
			return nil, err
		}
		if record.Run != run {
			return nil, fmt.Errorf("stage %s belongs to a different release Run", entry.Name())
		}
		if filepath.Base(stageRecordPath(root, run.ID, record)) != entry.Name() {
			return nil, fmt.Errorf("stage filename %s does not match its record", entry.Name())
		}
		records = append(records, record)
	}
	return records, nil
}

func loadStageRecord(path string) (_ stageRecord, returnErr error) {
	file, err := os.Open(path)
	if err != nil {
		return stageRecord{}, fmt.Errorf("open stage record: %w", err)
	}
	defer func() {
		if err := file.Close(); err != nil && returnErr == nil {
			returnErr = err
		}
	}()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var record stageRecord
	if err := decoder.Decode(&record); err != nil {
		return stageRecord{}, fmt.Errorf("decode stage record %s: %w", path, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return stageRecord{}, fmt.Errorf("stage record %s contains trailing JSON", path)
	}
	if err := record.validate(); err != nil {
		return stageRecord{}, fmt.Errorf("validate stage record %s: %w", path, err)
	}
	return record, nil
}

func stageRecordPath(root, runID string, record stageRecord) string {
	return filepath.Join(
		releasecontract.RunDirectory(root, runID),
		"artifacts",
		"platform",
		"stages",
		stageBase(record.Platform, record.Name, record.Attempt)+".json",
	)
}

func artifactForPath(root, runID, kind, filePath string) (releasecontract.Artifact, error) {
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		return releasecontract.Artifact{}, err
	}
	relative, err := filepath.Rel(root, absPath)
	if err != nil {
		return releasecontract.Artifact{}, err
	}
	relative = filepath.ToSlash(relative)
	requiredPrefix := releasecontract.RunRelativeDir(runID) + "/"
	if !strings.HasPrefix(relative, requiredPrefix) {
		return releasecontract.Artifact{}, fmt.Errorf("artifact %s must stay below %s", relative, requiredPrefix)
	}
	file, err := os.Open(absPath)
	if err != nil {
		return releasecontract.Artifact{}, err
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, file)
	closeErr := file.Close()
	if copyErr != nil {
		return releasecontract.Artifact{}, copyErr
	}
	if closeErr != nil {
		return releasecontract.Artifact{}, closeErr
	}
	return releasecontract.Artifact{
		Kind:   kind,
		Path:   relative,
		SHA256: hex.EncodeToString(hash.Sum(nil)),
	}, nil
}

func writeExclusiveJSON(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	return errors.Join(writeErr, closeErr)
}

func oneLine(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	const maxReasonLength = 500
	if len(value) > maxReasonLength {
		return value[:maxReasonLength] + "..."
	}
	return value
}
