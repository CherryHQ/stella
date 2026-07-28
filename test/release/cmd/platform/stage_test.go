package main

import (
	"errors"
	"io"
	"path/filepath"
	"testing"
	"time"

	releasecontract "github.com/CherryHQ/stella/test/release"
)

func TestRecordAndFinalizePlatformStages(t *testing.T) {
	root := t.TempDir()
	run := releasecontract.Run{
		ID:      "platform-test",
		Version: "v1.2.3",
		Commit:  "0123456789abcdef0123456789abcdef01234567",
	}
	startedAt := time.Date(2026, time.July, 27, 10, 0, 0, 0, time.UTC)
	expected := []stageExpectation{
		{Platform: releasecontract.Platform{OS: "linux", Arch: "amd64"}, Name: "binary-system"},
		{Platform: releasecontract.Platform{OS: "linux", Arch: "amd64"}, Name: "systemd"},
		{Platform: releasecontract.Platform{OS: "linux", Arch: "amd64"}, Name: "docker"},
		{Platform: releasecontract.Platform{OS: "linux", Arch: "amd64"}, Name: "helm"},
		{Platform: releasecontract.Platform{OS: "linux", Arch: "arm64"}, Name: "binary-system"},
		{Platform: releasecontract.Platform{OS: "linux", Arch: "arm64"}, Name: "docker"},
	}
	for _, stage := range expected {
		if err := recordStage(root, run, stage.Platform, stage.Name, 1, startedAt, func(log io.Writer) error {
			_, err := io.WriteString(log, "stage passed\n")
			return err
		}); err != nil {
			t.Fatalf("record %s/%s %s: %v", stage.Platform.OS, stage.Platform.Arch, stage.Name, err)
		}
	}
	if err := finalizePlatformResults(root, run, 1, startedAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	results, err := releasecontract.LoadResults(filepath.Join(releasecontract.RunDirectory(root, run.ID), "results"))
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("result count = %d, want 2", len(results))
	}
	for _, result := range results {
		if result.Status != releasecontract.StatusPass {
			t.Fatalf("%s status = %s, want pass", result.ScenarioID, result.Status)
		}
		if err := releasecontract.ValidateArtifactFiles(root, result); err != nil {
			t.Fatalf("%s artifacts: %v", result.ScenarioID, err)
		}
	}
}

func TestFinalizePlatformStagesRecordsMissingStageAsProductFailure(t *testing.T) {
	root := t.TempDir()
	run := releasecontract.Run{
		ID:      "platform-missing",
		Version: "v1.2.3",
		Commit:  "0123456789abcdef0123456789abcdef01234567",
	}
	now := time.Date(2026, time.July, 27, 10, 0, 0, 0, time.UTC)
	err := finalizePlatformResults(root, run, 1, now)
	if err == nil {
		t.Fatal("finalize succeeded without any platform stages")
	}
	results, loadErr := releasecontract.LoadResults(filepath.Join(releasecontract.RunDirectory(root, run.ID), "results"))
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if len(results) != 2 {
		t.Fatalf("result count = %d, want 2", len(results))
	}
	for _, result := range results {
		if result.Status != releasecontract.StatusProductFailure {
			t.Fatalf("%s status = %s, want product_failure", result.ScenarioID, result.Status)
		}
	}
}

func TestRecordStagePersistsFailureBeforeReturning(t *testing.T) {
	root := t.TempDir()
	run := releasecontract.Run{
		ID:      "platform-failure",
		Version: "v1.2.3",
		Commit:  "0123456789abcdef0123456789abcdef01234567",
	}
	platform := releasecontract.Platform{OS: "linux", Arch: "amd64"}
	err := recordStage(
		root,
		run,
		platform,
		"docker",
		1,
		time.Now().UTC(),
		func(io.Writer) error { return errors.New("expected failure") },
	)
	if err == nil {
		t.Fatal("recordStage succeeded for a failed command")
	}
	records, loadErr := loadStageRecords(root, run)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if len(records) != 1 || records[0].Status != releasecontract.StatusProductFailure {
		t.Fatalf("records = %+v, want one product failure", records)
	}
}
