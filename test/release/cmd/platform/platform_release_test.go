//go:build releaseplatform

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	releasecontract "github.com/CherryHQ/stella/test/release"
)

// TestReleaseArchiveMatrix inspects all six real GoReleaser archives and their
// checksum entries. The release workflow also emits its canonical X18-S01
// result through the archives command mode.
func TestReleaseArchiveMatrix(t *testing.T) {
	root, run, manifest := releaseFixture(t)
	if err := releasecontract.VerifyCandidateManifest(root, manifest, run); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyArchiveMatrix(root, manifest); err != nil {
		t.Fatal(err)
	}
}

// TestReleaseDockerCandidate runs the native platform's exact image digest
// against a fresh network-reachable Stella PG Runtime.
func TestReleaseDockerCandidate(t *testing.T) {
	root, run, manifest := releaseFixture(t)
	platform := releasecontract.Platform{OS: runtime.GOOS, Arch: runtime.GOARCH}
	if err := runDockerSmoke(root, run, manifest, platform, releaseAttemptForTest(t), os.Stdout); err != nil {
		t.Fatal(err)
	}
}

// TestReleaseHelmCandidate installs the exact amd64 candidate digest into kind,
// probes it, and proves that uninstall removes the deployment and cluster.
func TestReleaseHelmCandidate(t *testing.T) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("Helm release deployment is intentionally linux/amd64-only")
	}
	root, run, manifest := releaseFixture(t)
	platform := releasecontract.Platform{OS: runtime.GOOS, Arch: runtime.GOARCH}
	if err := runHelmSmoke(root, run, manifest, platform, releaseAttemptForTest(t), os.Stdout); err != nil {
		t.Fatal(err)
	}
}

// TestReleaseSystemdCandidate delegates to the destructive lifecycle script
// only on the dedicated ephemeral Linux amd64 release runner.
func TestReleaseSystemdCandidate(t *testing.T) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("systemd release lifecycle is intentionally linux/amd64-only")
	}
	candidate := os.Getenv("STELLA_SYSTEM_BINARY")
	if candidate == "" {
		t.Fatal("STELLA_SYSTEM_BINARY is required")
	}
	root, _, _ := releaseFixture(t)
	cmd := exec.Command("bash", filepath.Join(root, "test", "release", "systemd-smoke.sh"), candidate)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
}

func releaseFixture(t *testing.T) (string, releasecontract.Run, releasecontract.CandidateManifest) {
	t.Helper()
	run, present, err := releasecontract.RunFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if !present {
		t.Fatal("release Run environment is required")
	}
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate release platform source")
	}
	root, err := filepath.Abs(filepath.Join(filepath.Dir(thisFile), "..", "..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := releasecontract.LoadCandidateManifest(
		filepath.Join(releasecontract.RunDirectory(root, run.ID), "candidate.json"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Run != run {
		t.Fatal("candidate manifest does not identify the current release Run")
	}
	return root, run, manifest
}

func releaseAttemptForTest(t *testing.T) int {
	t.Helper()
	attempt, err := releaseAttempt()
	if err != nil {
		t.Fatal(err)
	}
	return attempt
}
