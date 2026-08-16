package sharedposix

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func qualifiedFixture(t *testing.T) (string, Config) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, stateDir)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	identity, _ := json.Marshal(identityFile{NamespaceIdentity: "namespace-a"})
	if err := os.WriteFile(filepath.Join(dir, "identity.json"), identity, 0o600); err != nil {
		t.Fatal(err)
	}
	record, _ := json.Marshal(struct {
		NamespaceIdentity string `json:"namespace_identity"`
		QualifiedShared   bool   `json:"qualified_shared"`
		OverallPass       bool   `json:"overall_pass"`
	}{"namespace-a", true, true})
	if err := os.WriteFile(filepath.Join(dir, "qualification.json"), record, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(record)
	return root, Config{
		Root:                root,
		NamespaceIdentity:   "namespace-a",
		QualificationSHA256: hex.EncodeToString(sum[:]),
		WitnessID:           "witness-a",
		CheckInterval:       10 * time.Millisecond,
		FreshnessTimeout:    80 * time.Millisecond,
		StartupTimeout:      200 * time.Millisecond,
	}
}

func startWitness(t *testing.T, root string) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- RunWitness(ctx, root, "witness-a", 10*time.Millisecond) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Errorf("witness stopped with %v", err)
		}
	})
	return cancel
}

func advanceWitness(t *testing.T, root string) {
	t.Helper()
	path := filepath.Join(root, stateDir, "witness.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var witness witnessFile
	if err := json.Unmarshal(data, &witness); err != nil {
		t.Fatal(err)
	}
	witness.Sequence++
	data, _ = json.Marshal(witness)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAdmissionRequiresAdvancingIndependentWitness(t *testing.T) {
	root, cfg := qualifiedFixture(t)
	startWitness(t, root)
	ctx := t.Context()
	a, err := New(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Check(t.Context()); err != nil {
		t.Fatalf("admission closed: %v", err)
	}
}

func TestAdmissionFailsClosedAndFullyRevalidates(t *testing.T) {
	root, cfg := qualifiedFixture(t)
	witnessCancel := startWitness(t, root)
	ctx := t.Context()
	a, err := New(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	witnessCancel()
	time.Sleep(cfg.CheckInterval)

	identityPath := filepath.Join(root, stateDir, "identity.json")
	wrong, _ := json.Marshal(identityFile{NamespaceIdentity: "wrong"})
	if err := os.WriteFile(identityPath, wrong, 0o600); err != nil {
		t.Fatal(err)
	}
	a.refresh()
	if !errors.Is(a.Check(t.Context()), ErrIdentity) {
		t.Fatalf("identity replacement did not close admission: %v", a.Check(t.Context()))
	}
	validIdentity, _ := json.Marshal(identityFile{NamespaceIdentity: "namespace-a"})
	if err := os.WriteFile(identityPath, validIdentity, 0o600); err != nil {
		t.Fatal(err)
	}
	a.refresh()
	if !errors.Is(a.Check(t.Context()), ErrStale) {
		t.Fatalf("identity recovery did not require fresh witness advancement: %v", a.Check(t.Context()))
	}
	advanceWitness(t, root)
	a.refresh()
	if err := a.Check(t.Context()); err != nil {
		t.Fatalf("full validation did not recover admission: %v", err)
	}
	qualificationPath := filepath.Join(root, stateDir, "qualification.json")
	qualification, err := os.ReadFile(qualificationPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(qualificationPath, append(qualification, ' '), 0o600); err != nil {
		t.Fatal(err)
	}
	a.refresh()
	if !errors.Is(a.Check(t.Context()), ErrQualification) {
		t.Fatalf("qualification mismatch did not close admission: %v", a.Check(t.Context()))
	}
	if err := os.WriteFile(qualificationPath, qualification, 0o600); err != nil {
		t.Fatal(err)
	}
	a.refresh()
	if !errors.Is(a.Check(t.Context()), ErrStale) {
		t.Fatalf("qualification recovery did not require fresh witness advancement: %v", a.Check(t.Context()))
	}
	advanceWitness(t, root)
	a.refresh()
	if err := a.Check(t.Context()); err != nil {
		t.Fatalf("qualification recovery did not fully revalidate: %v", err)
	}

	time.Sleep(cfg.FreshnessTimeout + cfg.CheckInterval)
	a.refresh()
	if !errors.Is(a.Check(t.Context()), ErrStale) {
		t.Fatalf("stopped witness did not close admission: %v", a.Check(t.Context()))
	}
}

func TestAdmissionRejectsRootReplacementUntilRestart(t *testing.T) {
	root, cfg := qualifiedFixture(t)
	stopWitness := startWitness(t, root)
	a, err := New(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	stopWitness()
	time.Sleep(cfg.CheckInterval)
	oldRoot := root + ".old"
	if err := os.Rename(root, oldRoot); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(oldRoot) })
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	a.refresh()
	if !errors.Is(a.Check(t.Context()), ErrMissing) {
		t.Fatalf("replacement root did not close admission: %v", a.Check(t.Context()))
	}
}

func TestAdmissionRejectsMissingAndUnqualifiedMounts(t *testing.T) {
	_, err := New(t.Context(), Config{Root: t.TempDir()})
	if err == nil {
		t.Fatal("missing shared storage configuration accepted")
	}
	root, cfg := qualifiedFixture(t)
	if err := os.Remove(filepath.Join(root, stateDir, "qualification.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := New(t.Context(), cfg); !errors.Is(err, ErrQualification) {
		t.Fatalf("missing qualification error = %v", err)
	}
}

func TestReadBoundedRejectsSymlinkAndOversizedEvidence(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("evidence"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readBounded(link, 32); err == nil {
		t.Fatal("symlinked evidence accepted")
	}
	if _, err := readBounded(target, 4); err == nil {
		t.Fatal("oversized evidence accepted")
	}
}
