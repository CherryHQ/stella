package sharedposix

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/storagequal"
)

func validQualificationRecord(identity string) storagequal.Record {
	limits := storagequal.Limits{MetadataP95MS: 10, SmallFilesP95MS: 20, ConcurrentP95MS: 30, StreamMiBPerSecond: 1, MinimumFreeBytes: 1}
	return storagequal.Record{
		SchemaVersion: 1, StartedUTC: "2026-08-16T00:00:00Z", EndedUTC: "2026-08-16T00:00:01Z",
		Backend: "test", BackendVersion: "1", Topology: "two test clients", Clients: 2, Nodes: 1,
		MountOptions: []string{"rw"}, NamespaceIdentity: identity, IdentityMechanism: "test identity", ReferenceHardware: "test host", IndependentMounts: true,
		IdentityEvidence: []storagequal.Result{{Name: "same_namespace_inode", Detail: "same object", Passed: true}, {Name: "distinct_client_root_objects", Detail: "distinct paths", Passed: true}, {Name: "declared_independent_mounts", Detail: "declared independent", Passed: true}},
		Conformance: []storagequal.Result{
			{Name: "atomic_same_directory_rename", Passed: true},
			{Name: "symlink_and_containment", Passed: true},
			{Name: "modes_and_ownership", Passed: true},
			{Name: "advisory_lock_across_clients", Passed: true},
			{Name: "atomic_append", Passed: true},
			{Name: "concurrent_read_write_no_torn_records", Passed: true},
			{Name: "close_to_open_a_to_b", Passed: true},
			{Name: "close_to_open_b_to_a", Passed: true},
			{Name: "fsync_file_and_directory", Passed: true},
		},
		Benchmarks: []storagequal.Benchmark{
			{Name: "typed_root_metadata_traversal", Unit: "ms_p95", Value: 1, Criterion: limits.MetadataP95MS, Comparison: "less_or_equal", Passed: true},
			{Name: "small_file_project_skill_publication", Unit: "ms_p95", Value: 1, Criterion: limits.SmallFilesP95MS, Comparison: "less_or_equal", Passed: true},
			{Name: "large_upload_share_streaming", Unit: "MiB_per_second", Value: 2, Criterion: limits.StreamMiBPerSecond, Comparison: "greater_or_equal", Passed: true},
			{Name: "concurrent_api_sandbox_access", Unit: "ms_p95", Value: 1, Criterion: limits.ConcurrentP95MS, Comparison: "less_or_equal", Passed: true},
			{Name: "free_capacity", Unit: "bytes", Value: 2, Criterion: float64(limits.MinimumFreeBytes), Comparison: "greater_or_equal", Passed: true},
		},
		Limits:           limits,
		FailureInjection: storagequal.FailureInjection{Injected: true, DisconnectObserved: true, Remounted: true, Revalidated: true, ErrorClass: "outcome_unknown", OutcomeUnknown: true, Detail: "test disconnect and recovery"},
		Readiness:        []storagequal.Transition{{State: "not_ready", Reason: "qualification_started"}, {State: "not_ready", Reason: "fault_injected"}, {State: "ready", Reason: "full_contract_validated"}},
		Recovery:         storagequal.Result{Name: "full_revalidation_after_remount", Detail: "test disconnect and recovery", Passed: true}, QualifiedShared: true, OverallPass: true,
	}
}

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
	record, _ := json.Marshal(validQualificationRecord("namespace-a"))
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

func TestAdmissionRejectsDigestMatchingPartialQualification(t *testing.T) {
	root, cfg := qualifiedFixture(t)
	path := filepath.Join(root, stateDir, "qualification.json")
	partial := validQualificationRecord("namespace-a")
	partial.Conformance = partial.Conformance[:1]
	data, err := json.Marshal(partial)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	cfg.QualificationSHA256 = hex.EncodeToString(digest[:])
	if _, err := New(t.Context(), cfg); !errors.Is(err, ErrQualification) {
		t.Fatalf("digest-matching partial qualification error = %v", err)
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

func TestStartupTimeoutBoundsBlockingValidation(t *testing.T) {
	_, cfg := qualifiedFixture(t)
	cfg.StartupTimeout = 40 * time.Millisecond
	entered := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	started := time.Now()
	_, err := newWithValidator(t.Context(), cfg, func() (uint64, error) {
		calls.Add(1)
		close(entered)
		<-release
		return 1, nil
	})
	if !errors.Is(err, ErrStale) {
		t.Fatalf("blocking startup validation error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 3*cfg.StartupTimeout {
		t.Fatalf("startup timeout took %v, configured %v", elapsed, cfg.StartupTimeout)
	}
	<-entered
	if got := calls.Load(); got != 1 {
		t.Fatalf("blocking startup launched %d validators, want 1", got)
	}
	close(release)
}

func TestRuntimeBlockingValidationBecomesStaleWithoutProbeStorm(t *testing.T) {
	_, cfg := qualifiedFixture(t)
	cfg.CheckInterval = 5 * time.Millisecond
	cfg.FreshnessTimeout = 30 * time.Millisecond
	cfg.StartupTimeout = 100 * time.Millisecond
	blocked := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	a, err := newWithValidator(ctx, cfg, func() (uint64, error) {
		call := calls.Add(1)
		if call == 3 {
			close(blocked)
			<-release
		}
		return uint64(call), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	<-blocked
	time.Sleep(cfg.FreshnessTimeout + 3*cfg.CheckInterval)
	if !errors.Is(a.Check(t.Context()), ErrStale) {
		t.Fatalf("hung runtime validation did not close admission: %v", a.Check(t.Context()))
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("hung runtime launched overlapping validators: calls=%d", got)
	}
	close(release)
	deadline := time.Now().Add(10 * cfg.FreshnessTimeout)
	for time.Now().Before(deadline) {
		if a.Check(t.Context()) == nil {
			return
		}
		time.Sleep(cfg.CheckInterval)
	}
	t.Fatalf("admission did not recover after full validation and fresh advance: %v", a.Check(t.Context()))
}
