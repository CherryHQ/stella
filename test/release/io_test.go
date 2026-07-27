package release

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteAndLoadResultsPreservesAttempts(t *testing.T) {
	runDir := filepath.Join(t.TempDir(), "release-1")
	first := validResult()
	first.Status = StatusExternalBlocked
	first.Reason = "provider maintenance"
	second := validResult()
	second.Attempt = 2
	second.Status = StatusFlaky
	second.Reason = "the retry passed after provider recovery"

	firstPath, err := WriteResult(runDir, first)
	if err != nil {
		t.Fatal(err)
	}
	secondPath, err := WriteResult(runDir, second)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(firstPath) != "release-1-C01-S01-a001.json" ||
		filepath.Base(secondPath) != "release-1-C01-S01-a002.json" {
		t.Fatalf("unexpected result paths: %s, %s", firstPath, secondPath)
	}

	results, err := LoadResults(filepath.Join(runDir, "results"))
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].Attempt != 1 || results[1].Attempt != 2 {
		t.Fatalf("unexpected results: %+v", results)
	}
}

func TestWriteResultDoesNotReplaceExistingAttempt(t *testing.T) {
	runDir := filepath.Join(t.TempDir(), "release-1")
	result := validResult()
	if _, err := WriteResult(runDir, result); err != nil {
		t.Fatal(err)
	}
	_, err := WriteResult(runDir, result)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("expected no-replace error, got %v", err)
	}
}

func TestLoadResultRejectsUnknownField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "result.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":1,"unexpected":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadResult(path)
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("expected unknown field error, got %v", err)
	}
}

func TestLoadResultsRejectsMisnamedResult(t *testing.T) {
	resultsDir := t.TempDir()
	result := validResult()
	path := filepath.Join(resultsDir, "copied-from-another-run.json")
	writeJSONFixture(t, path, result)

	_, err := LoadResults(resultsDir)
	if err == nil || !strings.Contains(err.Error(), "must be release-1-C01-S01-a001.json") {
		t.Fatalf("expected filename error, got %v", err)
	}
}
