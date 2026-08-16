//go:build unix

package storagequal

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testConfig(a, b string) Config {
	return Config{
		ClientA: a, ClientB: b,
		Metadata:         Metadata{Backend: "local-posix-control", Version: "1", Topology: "single-host-alias", Clients: 2, Nodes: 1, NamespaceIdentity: "test", IdentityMechanism: "cross-client inode probe", ReferenceHardware: "orb local filesystem", IndependentMounts: true},
		Limits:           Limits{MetadataP95MS: 10_000, SmallFilesP95MS: 10_000, ConcurrentP95MS: 10_000, StreamMiBPerSecond: .001, MinimumFreeBytes: 1},
		FailureInjection: &FailureInjection{Injected: true, DisconnectObserved: true, Remounted: true, Revalidated: true, ErrorClass: "outcome_unknown", OutcomeUnknown: true},
	}
}

func TestLocalPOSIXControlPassesSemanticsButIsNotShared(t *testing.T) {
	root := t.TempDir()
	alias := filepath.Join(t.TempDir(), "client-b")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(root, alias)
	fixed := time.Date(2026, 8, 16, 1, 2, 3, 0, time.UTC)
	cfg.Now = func() time.Time { return fixed }
	record, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range record.Conformance {
		if !result.Passed {
			t.Errorf("%s failed: %s", result.Name, result.Detail)
		}
	}
	if record.OverallPass || record.QualifiedShared {
		t.Fatal("aliased local paths were incorrectly qualified as shared")
	}
	one, err := record.JSON()
	if err != nil {
		t.Fatal(err)
	}
	two, _ := record.JSON()
	if string(one) != string(two) || !json.Valid(one) {
		t.Fatal("record JSON is not deterministic and valid")
	}
}

func TestDivergentRootsFailClosed(t *testing.T) {
	_, err := Run(context.Background(), testConfig(t.TempDir(), t.TempDir()))
	if err == nil {
		t.Fatal("divergent namespaces accepted")
	}
}

func TestReadOnlyRootFailsClosed(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory write permission")
	}
	root := t.TempDir()
	alias := filepath.Join(t.TempDir(), "client-b")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o500); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(root, 0o700) }()
	if _, err := Run(context.Background(), testConfig(root, alias)); err == nil {
		t.Fatal("read-only namespace accepted")
	}
}

func TestCriteriaAndFailureEvidenceRequired(t *testing.T) {
	cfg := testConfig(t.TempDir(), t.TempDir())
	cfg.Limits.StreamMiBPerSecond = 0
	if _, err := Run(context.Background(), cfg); err == nil {
		t.Fatal("missing predeclared criterion accepted")
	}
	cfg = testConfig(t.TempDir(), t.TempDir())
	cfg.FailureInjection = nil
	if _, err := Run(context.Background(), cfg); err == nil {
		t.Fatal("missing failure injection accepted")
	}
}
