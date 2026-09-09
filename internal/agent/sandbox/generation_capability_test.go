package sandbox

import (
	"context"
	"errors"
	"io/fs"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/agentrun"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

func TestGenerationCapabilityMaxRetainedOneReopensIdleGeneration(t *testing.T) {
	db, sessionID, owner, _ := acceptanceGenerationDB(t)
	store := NewGenerationStore(db, owner, nil)
	store.maxRetained = 1

	raw := &acceptanceResource{
		Session:  pkgsandbox.NopSession(),
		identity: pkgsandbox.ResourceIdentity{Backend: "none"},
	}
	t.Cleanup(func() { _ = raw.Close() })
	var creates atomic.Int32
	spec := GenerationSpec{
		SessionID:    sessionID,
		Backend:      "none",
		ConfigDigest: "same-policy",
		Create: func(context.Context, int64, string) (pkgsandbox.Session, error) {
			creates.Add(1)
			return raw, nil
		},
	}

	first, err := store.Open(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("idle runner retirement: %v", err)
	}
	second, err := store.Open(t.Context(), spec)
	if err != nil {
		t.Fatalf("same-generation idle reopen: %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })

	row, err := store.Inspect(t.Context(), sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if row.Generation != 1 || row.State != GenerationActive || creates.Load() != 1 {
		t.Fatalf("idle reopen created or fenced a generation: row=%+v creates=%d", row, creates.Load())
	}

	otherSessionID := uuid.NewString()
	if _, err := db.Exec(t.Context(), `INSERT INTO ctx_conversation(session_id) VALUES ($1)`, otherSessionID); err != nil {
		t.Fatal(err)
	}
	otherSpec := spec
	otherSpec.SessionID = otherSessionID
	if _, err := store.Open(t.Context(), otherSpec); !errors.Is(err, ErrGenerationCapacity) {
		t.Fatalf("another Session at maxRetained=1 = %v, want ErrGenerationCapacity", err)
	}
	if got := creates.Load(); got != 1 {
		t.Fatalf("capacity rejection invoked backend Create, creates=%d", got)
	}
	var creating int
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM agent_sandbox_generation WHERE session_id=$1 AND state='creating'`, otherSessionID).Scan(&creating); err != nil {
		t.Fatal(err)
	}
	if creating != 0 {
		t.Fatalf("capacity rejection left %d creating rows for the other Session", creating)
	}
}

func TestGenerationCapabilityUnknownExecNoReplayAllowsControllerRecovery(t *testing.T) {
	db, sessionID, owner, _ := acceptanceGenerationDB(t)
	controller := &acceptanceController{}
	unknown := errors.New("provider accepted command but result was lost")
	firstRaw := &acceptanceResource{
		Session:  &capabilitySession{Session: pkgsandbox.NopSession(), execErr: unknown},
		identity: pkgsandbox.ResourceIdentity{Backend: "capability-test", Authority: "authority", Ref: "first"},
	}
	secondRaw := &acceptanceResource{
		Session:  &capabilitySession{Session: pkgsandbox.NopSession()},
		identity: pkgsandbox.ResourceIdentity{Backend: "capability-test", Authority: "authority", Ref: "second"},
	}
	t.Cleanup(func() {
		_ = firstRaw.Close()
		_ = secondRaw.Close()
	})
	registry, err := NewBackendRegistry(BackendDefinition{
		Name: "capability-test",
		Create: func(context.Context, BackendRequest) (pkgsandbox.Session, error) {
			return secondRaw, nil
		},
		ControllerFactory: func(context.Context) (pkgsandbox.ResourceController, error) {
			return controller, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	store := NewGenerationStore(db, owner, registry)
	var creates atomic.Int32
	spec := GenerationSpec{
		SessionID:    sessionID,
		Backend:      "capability-test",
		ConfigDigest: "same-policy",
		Create: func(context.Context, int64, string) (pkgsandbox.Session, error) {
			switch creates.Add(1) {
			case 1:
				return firstRaw, nil
			default:
				return secondRaw, nil
			}
		},
	}

	managed, err := store.Open(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	result, err := managed.Exec(t.Context(), "may-have-run", pkgsandbox.ExecOptions{})
	if !errors.Is(err, unknown) || result != (pkgsandbox.ExecResult{}) {
		t.Fatalf("unknown raw Exec = %+v, %v", result, err)
	}
	if got := firstRaw.Session.(*capabilitySession).execs.Load(); got != 1 {
		t.Fatalf("first raw Exec calls = %d, want 1", got)
	}

	// The second operation must stop at the local unknown fence. It must not
	// ask ResilientSession to create a replacement or replay the command.
	_, err = managed.Exec(t.Context(), "must-not-replay", pkgsandbox.ExecOptions{})
	if err == nil || !errors.Is(err, ErrGenerationUnknown) {
		t.Fatalf("second Exec after unknown = %v, want ErrGenerationUnknown", err)
	}
	if errors.Is(err, unknown) {
		t.Fatalf("second Exec replayed the raw unknown operation: %v", err)
	}
	if got := creates.Load(); got != 1 {
		t.Fatalf("second Exec recreated a generation, creates=%d", got)
	}
	if got := firstRaw.Session.(*capabilitySession).execs.Load(); got != 1 {
		t.Fatalf("second Exec reached the raw backend, calls=%d", got)
	}
	row, err := store.Inspect(t.Context(), sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if row.State != GenerationUnknown {
		t.Fatalf("unknown raw Exec state = %s, want unknown", row.State)
	}
	if err := managed.Close(); err != nil {
		t.Fatalf("release fenced runner: %v", err)
	}

	// The raw backend remains locally alive, but the durable unknown row is
	// recoverable only through its controller's probe/terminate proof.
	recovered, err := store.Open(t.Context(), spec)
	if err != nil {
		t.Fatalf("controller-backed recovery after unknown: %v", err)
	}
	t.Cleanup(func() { _ = recovered.Close() })
	row, err = store.Inspect(t.Context(), sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if row.Generation != 2 || row.State != GenerationActive || creates.Load() != 2 {
		t.Fatalf("recovery generation = %+v, creates=%d", row, creates.Load())
	}
	if got := controller.terminations.Load(); got != 1 {
		t.Fatalf("controller terminations = %d, want 1", got)
	}
	if _, err := recovered.Exec(t.Context(), "fresh-generation", pkgsandbox.ExecOptions{}); err != nil {
		t.Fatalf("recovered generation Exec: %v", err)
	}
	if got := secondRaw.Session.(*capabilitySession).execs.Load(); got != 1 {
		t.Fatalf("recovered raw Exec calls = %d, want 1", got)
	}
}

func TestGenerationCapabilityNotStartedDoesNotBlockCurrentGeneration(t *testing.T) {
	db, sessionID, owner, _ := acceptanceGenerationDB(t)
	preflight := pkgsandbox.MarkNotStarted(errors.New("invalid cwd"))
	rawSession := &capabilitySession{Session: pkgsandbox.NopSession(), execErr: preflight}
	raw := &acceptanceResource{
		Session:  rawSession,
		identity: pkgsandbox.ResourceIdentity{Backend: "preflight-test"},
	}
	t.Cleanup(func() { _ = raw.Close() })
	store := NewGenerationStore(db, owner, nil)
	var creates atomic.Int32
	spec := GenerationSpec{
		SessionID:    sessionID,
		Backend:      "preflight-test",
		ConfigDigest: "same-policy",
		Create: func(context.Context, int64, string) (pkgsandbox.Session, error) {
			creates.Add(1)
			return raw, nil
		},
	}
	managed, err := store.Open(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		if _, err := managed.Exec(t.Context(), "preflight-only", pkgsandbox.ExecOptions{}); !errors.Is(err, pkgsandbox.ErrNotStarted) {
			t.Fatalf("pre-start Exec %d = %v, want ErrNotStarted", i+1, err)
		}
	}
	if got := rawSession.execs.Load(); got != 2 {
		t.Fatalf("NotStarted blocked or recreated the generation, raw calls=%d", got)
	}
	row, err := store.Inspect(t.Context(), sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if row.State != GenerationActive || creates.Load() != 1 {
		t.Fatalf("NotStarted changed generation state: row=%+v creates=%d", row, creates.Load())
	}
}

func TestGenerationCapabilityFileViewRejectsExpiredRunWrite(t *testing.T) {
	db, sessionID, owner := capabilityLeaseDB(t)
	runStore := agentrun.NewStoreWithLease(t.Context(), db, owner, time.Hour)
	t.Cleanup(runStore.Close)
	if err := runStore.RegisterBoot(t.Context()); err != nil {
		t.Fatal(err)
	}
	lease, err := runStore.Acquire(t.Context(), sessionID, "sandbox-capability")
	if err != nil {
		t.Fatal(err)
	}

	files := &capabilityFiles{FileAccess: pkgsandbox.NopSession().Files()}
	raw := &acceptanceResource{
		Session:  &capabilitySession{Session: pkgsandbox.NopSession(), files: files},
		identity: pkgsandbox.ResourceIdentity{Backend: "file-view-test"},
	}
	t.Cleanup(func() { _ = raw.Close() })
	store := NewGenerationStore(db, owner, nil)
	spec := GenerationSpec{
		SessionID:    sessionID,
		Backend:      "file-view-test",
		ConfigDigest: "same-policy",
		Create: func(context.Context, int64, string) (pkgsandbox.Session, error) {
			return raw, nil
		},
	}
	managed, err := store.Open(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	view, err := pkgsandbox.SelectFileView(lease.Context(), managed)
	if err != nil {
		t.Fatalf("select file view: %v", err)
	}
	if err := view.Files.WriteFile("/before", []byte("before"), 0o600); err != nil {
		t.Fatalf("write before lease expiry: %v", err)
	}

	if _, err := db.Exec(t.Context(), `UPDATE agent_run SET lease_expires_at = clock_timestamp() - interval '1 second' WHERE id = $1`, lease.Guard.RunID); err != nil {
		t.Fatal(err)
	}
	if err := view.Files.WriteFile("/after", []byte("after"), 0o600); !errors.Is(err, agentrun.ErrLeaseLost) {
		t.Fatalf("stale file view write = %v, want ErrLeaseLost", err)
	}
	if got := files.writes.Load(); got != 1 {
		t.Fatalf("stale file view reached backend, writes=%d", got)
	}
}

func capabilityLeaseDB(t *testing.T) (*pgxpool.Pool, string, string) {
	t.Helper()
	db := dbtest.New(t)
	sessionID, owner := uuid.NewString(), uuid.NewString()
	if _, err := db.Exec(t.Context(), `INSERT INTO ctx_conversation(session_id) VALUES ($1)`, sessionID); err != nil {
		t.Fatal(err)
	}
	return db, sessionID, owner
}

type capabilitySession struct {
	pkgsandbox.Session
	files   pkgsandbox.FileAccess
	execErr error
	execs   atomic.Int32
	mu      sync.Mutex
}

func (s *capabilitySession) Exec(context.Context, string, pkgsandbox.ExecOptions) (pkgsandbox.ExecResult, error) {
	s.execs.Add(1)
	return pkgsandbox.ExecResult{}, s.execErr
}

func (s *capabilitySession) Files() pkgsandbox.FileAccess {
	if s.files != nil {
		return s.files
	}
	return s.Session.Files()
}

func (s *capabilitySession) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Session == nil {
		return nil
	}
	err := s.Session.Close()
	s.Session = nil
	return err
}

type capabilityFiles struct {
	pkgsandbox.FileAccess
	writes atomic.Int32
}

func (f *capabilityFiles) WriteFile(string, []byte, fs.FileMode) error {
	f.writes.Add(1)
	return nil
}
