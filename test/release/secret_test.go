package release

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScanForSecretsFindsCanaryAcrossReadBoundary(t *testing.T) {
	root := t.TempDir()
	secret := "canary-release-secret"
	content := strings.Repeat("x", secretScanChunkSize-5) + secret + "\n"
	if err := os.WriteFile(filepath.Join(root, "trace.zip"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	err := ScanForSecrets(root, map[string]string{"CANARY_SECRET": secret})
	if err == nil || !strings.Contains(err.Error(), "CANARY_SECRET") {
		t.Fatalf("expected canary detection, got %v", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("secret value leaked in error: %v", err)
	}
}

func TestCheckBytesForSecretsRejectsGeneratedReport(t *testing.T) {
	secret := "report-canary-secret"
	err := CheckBytesForSecrets("release report", []byte("reason: "+secret), map[string]string{"REPORT_SECRET": secret})
	if err == nil || !strings.Contains(err.Error(), "REPORT_SECRET") {
		t.Fatalf("expected report canary detection, got %v", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("secret value leaked in error: %v", err)
	}
}

func TestScanForSecretsDoesNotEchoSecretFromPath(t *testing.T) {
	root := t.TempDir()
	secret := "path-canary-secret"
	if err := os.WriteFile(filepath.Join(root, "trace-"+secret+".zip"), []byte("safe content"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := ScanForSecrets(root, map[string]string{"PATH_SECRET": secret})
	if err == nil || !strings.Contains(err.Error(), "PATH_SECRET") {
		t.Fatalf("expected path canary detection, got %v", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("secret value leaked in path error: %v", err)
	}
}

func TestRunFromEnvRejectsPartialMetadata(t *testing.T) {
	t.Setenv(EnvRunID, "release-1")
	t.Setenv(EnvVersion, "v1.2.3")
	t.Setenv(EnvCommit, "")

	_, present, err := RunFromEnv()
	if err == nil || present || !strings.Contains(err.Error(), EnvCommit) {
		t.Fatalf("expected missing commit error, got present=%v err=%v", present, err)
	}
}

func TestPresentSecretValuesFromEnvSkipsUnconfiguredTargets(t *testing.T) {
	t.Setenv("CONFIGURED_LIVE_SECRET", "canary")
	if err := os.Unsetenv("MISSING_LIVE_SECRET"); err != nil {
		t.Fatal(err)
	}
	values, err := PresentSecretValuesFromEnv([]string{"CONFIGURED_LIVE_SECRET", "MISSING_LIVE_SECRET"})
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 1 || values["CONFIGURED_LIVE_SECRET"] != "canary" {
		t.Fatalf("unexpected present secrets: %v", values)
	}
}

func writeJSONFixture(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}
