//go:build system

package system

import (
	"path/filepath"
	"testing"

	releasecontract "github.com/CherryHQ/stella/test/release"
)

// TestReleaseMetadataRoutesSystemLogs verifies that release diagnostics use
// the same canonical Run directory consumed by the result aggregator.
func TestReleaseMetadataRoutesSystemLogs(t *testing.T) {
	t.Setenv(releasecontract.EnvRunID, "release-metadata-fixture")
	t.Setenv(releasecontract.EnvVersion, "v1.2.3")
	t.Setenv(releasecontract.EnvCommit, "0123456789abcdef0123456789abcdef01234567")

	got := logDir(t)
	want := filepath.Join(
		releasecontract.RunDirectory(repoRoot(t), "release-metadata-fixture"),
		"artifacts",
		"system",
	)
	if got != want {
		t.Fatalf("logDir() = %s, want %s", got, want)
	}
}
