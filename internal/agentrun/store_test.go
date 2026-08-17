package agentrun_test

import (
	"context"
	"errors"
	"io/fs"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	. "github.com/CherryHQ/stella/internal/agentrun"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

func newTestStore(t *testing.T, db *pgxpool.Pool, cleaner ...func(context.Context, string, string) error) *Store {
	t.Helper()
	bootID := NewBootID()
	if _, err := sqlc.New(db).CreateExecutorBoot(t.Context(), sqlc.CreateExecutorBootParams{ID: bootID}); err != nil {
		t.Fatalf("register test executor boot: %v", err)
	}
	return NewStore(db, bootID, cleaner...)
}

func createSession(t *testing.T, q *sqlc.Queries, sessionID string) {
	t.Helper()
	_, err := q.CreateConversation(t.Context(), sqlc.CreateConversationParams{
		ID: uuid.Must(uuid.NewV7()).String(), SessionID: sessionID, Channel: "web", Kind: "chat",
		LastActive: time.Now().UTC(), AgentID: pgtype.Text{String: "agent", Valid: true},
		UserID: pgtype.Text{String: uuid.Must(uuid.NewV7()).String(), Valid: true},
	})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
}

func TestIndependentStoresRaceOneAdmissionWinner(t *testing.T) {
	db := dbtest.New(t)
	q := sqlc.New(db)
	createSession(t, q, "session-race")
	stores := []*Store{newTestStore(t, db), newTestStore(t, db)}
	start := make(chan struct{})
	var winners atomic.Int32
	var winner *Lease
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, store := range stores {
		wg.Add(1)
		go func(store *Store) {
			defer wg.Done()
			<-start
			lease, err := store.Acquire(context.Background(), "session-race", "web")
			if err == nil {
				winners.Add(1)
				mu.Lock()
				winner = lease
				mu.Unlock()
				return
			}
			if !errors.Is(err, ErrBusy) {
				t.Errorf("Acquire: %v", err)
			}
		}(store)
	}
	close(start)
	wg.Wait()
	if got := winners.Load(); got != 1 {
		t.Fatalf("admission winners = %d, want 1", got)
	}
	if err := winner.Finish(t.Context(), StatusCompleted, ""); err != nil {
		t.Fatalf("finish winner: %v", err)
	}
}

func TestAbortAndCompletionHaveOneLinearWinner(t *testing.T) {
	db := dbtest.New(t)
	q := sqlc.New(db)
	createSession(t, q, "session-terminal")
	store := newTestStore(t, db)
	lease, err := store.Acquire(t.Context(), "session-terminal", "web")
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		_ = lease.Finish(context.Background(), StatusCompleted, "")
	}()
	go func() {
		defer wg.Done()
		<-start
		_, _ = store.RequestAbort(context.Background(), "session-terminal", "test_abort")
	}()
	close(start)
	wg.Wait()
	run, err := q.GetAgentRun(t.Context(), lease.Guard.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != StatusCompleted && run.Status != StatusAborted {
		t.Fatalf("terminal status = %q", run.Status)
	}
	if !run.CompletedAt.Valid {
		t.Fatal("terminal run has no completed_at")
	}
}

func TestStaleOwnerCannotValidateWriteAfterReplacement(t *testing.T) {
	db := dbtest.New(t)
	q := sqlc.New(db)
	createSession(t, q, "session-stale")
	firstStore := newTestStore(t, db)
	first, err := firstStore.Acquire(t.Context(), "session-stale", "web")
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Finish(t.Context(), StatusCompleted, ""); err != nil {
		t.Fatal(err)
	}
	second, err := newTestStore(t, db).Acquire(t.Context(), "session-stale", "web")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()
	if err := ValidateTx(WithGuard(t.Context(), first.Guard), tx); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale ValidateTx = %v, want ErrLeaseLost", err)
	}
	if err := second.Finish(t.Context(), StatusCompleted, ""); err != nil {
		t.Fatal(err)
	}
}

func TestValidatedSourceWriteLinearizesBeforeAbort(t *testing.T) {
	db := dbtest.New(t)
	q := sqlc.New(db)
	createSession(t, q, "session-write-abort")
	store := newTestStore(t, db)
	lease, err := store.Acquire(t.Context(), "session-write-abort", "web")
	if err != nil {
		t.Fatal(err)
	}

	tx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()
	guarded := WithGuard(t.Context(), lease.Guard)
	if err := ValidateTx(guarded, tx); err != nil {
		t.Fatalf("validate source write: %v", err)
	}
	if _, err := tx.Exec(guarded, `UPDATE ctx_conversation SET title = 'committed before abort' WHERE session_id = $1`, "session-write-abort"); err != nil {
		t.Fatalf("write source row: %v", err)
	}

	abortDone := make(chan error, 1)
	go func() {
		_, err := store.RequestAbort(context.Background(), "session-write-abort", "test_abort")
		abortDone <- err
	}()
	select {
	case err := <-abortDone:
		t.Fatalf("abort passed transaction-coupled source-write fence: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	if err := tx.Commit(t.Context()); err != nil {
		t.Fatalf("commit source write: %v", err)
	}
	select {
	case err := <-abortDone:
		if err != nil {
			t.Fatalf("request abort: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("abort did not proceed after source write committed")
	}

	var title string
	if err := db.QueryRow(t.Context(), `SELECT title FROM ctx_conversation WHERE session_id = $1`, "session-write-abort").Scan(&title); err != nil {
		t.Fatal(err)
	}
	if title != "committed before abort" {
		t.Fatalf("source write title = %q", title)
	}
	staleTx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = staleTx.Rollback(t.Context()) }()
	if err := ValidateTx(guarded, staleTx); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("post-abort ValidateTx = %v, want ErrLeaseLost", err)
	}
}

func TestInboxAdmissionLinksRunAtomically(t *testing.T) {
	db := dbtest.New(t)
	q := sqlc.New(db)
	createSession(t, q, "source-inbox")
	createSession(t, q, "target-inbox")
	inboxID := uuid.Must(uuid.NewV7()).String()
	if _, err := q.EnqueueSessionInbox(t.Context(), sqlc.EnqueueSessionInboxParams{
		ID: inboxID, SourceSessionID: "source-inbox", TargetSessionID: "target-inbox",
		ActorID: "agent", Content: "hello",
	}); err != nil {
		t.Fatal(err)
	}
	store := newTestStore(t, db)
	if _, err := store.AcquireForInbox(t.Context(), "target-inbox", "session", uuid.Must(uuid.NewV7()).String()); err == nil {
		t.Fatal("admission with unknown inbox unexpectedly succeeded")
	}
	if _, running, err := store.Running(t.Context(), "target-inbox"); err != nil || running {
		t.Fatalf("failed linked admission left running Run: running=%v err=%v", running, err)
	}
	lease, err := store.AcquireForInbox(t.Context(), "target-inbox", "session", inboxID)
	if err != nil {
		t.Fatal(err)
	}
	row, err := q.GetSessionInbox(t.Context(), inboxID)
	if err != nil {
		t.Fatal(err)
	}
	if !row.RunID.Valid || row.RunID.String != lease.Guard.RunID {
		t.Fatalf("inbox run_id = %#v, want %s", row.RunID, lease.Guard.RunID)
	}
	if err := lease.Finish(t.Context(), StatusCompleted, ""); err != nil {
		t.Fatal(err)
	}
}

func TestAcquireForChannelFIFOAtomicallyLinksClaim(t *testing.T) {
	db := dbtest.New(t)
	q := sqlc.New(db)
	createSession(t, q, "target-channel")
	if _, err := db.Exec(t.Context(), `INSERT INTO channel (id, name, type, enabled, config) VALUES ('channel-fifo', 'FIFO', 'web', true, '{}')`); err != nil {
		t.Fatal(err)
	}
	row, err := q.CreateChannelBindingFIFO(t.Context(), sqlc.CreateChannelBindingFIFOParams{
		ID: uuid.Must(uuid.NewV7()).String(), ChannelID: "channel-fifo", BindingKey: "binding",
		SourceKey: "web:chat:message", Kind: "message", Payload: []byte(`[]`), ImmutableMedia: []byte(`[]`),
	})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := q.ClaimChannelBindingFIFOHead(t.Context(), row.ID)
	if err != nil {
		t.Fatal(err)
	}
	store := newTestStore(t, db)
	lease, err := store.AcquireForChannelFIFO(t.Context(), "target-channel", "channel", claimed.ID, claimed.ClaimToken.String)
	if err != nil {
		t.Fatal(err)
	}
	linked, err := q.GetChannelBindingFIFO(t.Context(), row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !linked.RunID.Valid || linked.RunID.String != lease.Guard.RunID {
		t.Fatalf("FIFO run_id = %#v, want %s", linked.RunID, lease.Guard.RunID)
	}
	if err := lease.Finish(t.Context(), StatusCompleted, ""); err != nil {
		t.Fatal(err)
	}
}

func TestAcquireForChannelFIFORejectsExpiredClaim(t *testing.T) {
	db := dbtest.New(t)
	q := sqlc.New(db)
	createSession(t, q, "target-expired-channel")
	if _, err := db.Exec(t.Context(), `INSERT INTO channel (id, name, type, enabled, config) VALUES ('channel-expired-fifo', 'FIFO', 'web', true, '{}')`); err != nil {
		t.Fatal(err)
	}
	row, err := q.CreateChannelBindingFIFO(t.Context(), sqlc.CreateChannelBindingFIFOParams{
		ID: uuid.Must(uuid.NewV7()).String(), ChannelID: "channel-expired-fifo", BindingKey: "binding",
		SourceKey: "web:chat:expired", Kind: "message", Payload: []byte(`[]`), ImmutableMedia: []byte(`[]`),
	})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := q.ClaimChannelBindingFIFOHead(t.Context(), row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(t.Context(), `UPDATE channel_binding_fifo SET claim_expires_at = now() - interval '1 second' WHERE id = $1`, row.ID); err != nil {
		t.Fatal(err)
	}
	store := newTestStore(t, db)
	if _, err := store.AcquireForChannelFIFO(t.Context(), "target-expired-channel", "channel", claimed.ID, claimed.ClaimToken.String); err == nil {
		t.Fatal("expired FIFO claimant unexpectedly admitted an AgentRun")
	}
	if _, running, err := store.Running(t.Context(), "target-expired-channel"); err != nil || running {
		t.Fatalf("expired claim left running Run: running=%v err=%v", running, err)
	}
}

func TestReapTerminalizesLinkedUndeliveredInboxInSamePass(t *testing.T) {
	db := dbtest.New(t)
	q := sqlc.New(db)
	createSession(t, q, "source-reap")
	createSession(t, q, "target-reap")
	inboxID := uuid.Must(uuid.NewV7()).String()
	if _, err := q.EnqueueSessionInbox(t.Context(), sqlc.EnqueueSessionInboxParams{
		ID: inboxID, SourceSessionID: "source-reap", TargetSessionID: "target-reap",
		ActorID: "agent", Content: "hello",
	}); err != nil {
		t.Fatal(err)
	}
	store := newTestStore(t, db)
	runCtx, cancelRun := context.WithCancel(t.Context())
	lease, err := store.AcquireForInbox(runCtx, "target-reap", "session", inboxID)
	if err != nil {
		t.Fatal(err)
	}
	cancelRun()
	<-lease.Context().Done()
	if _, err := db.Exec(t.Context(), `UPDATE agent_run SET lease_expires_at = now() - interval '1 second' WHERE id = $1`, lease.Guard.RunID); err != nil {
		t.Fatal(err)
	}
	if err := store.Reap(t.Context()); err != nil {
		t.Fatal(err)
	}
	row, err := q.GetSessionInbox(t.Context(), inboxID)
	if err != nil {
		t.Fatal(err)
	}
	if !row.FailedAt.Valid || row.ErrorCode != "run_interrupted" {
		t.Fatalf("linked inbox not terminalized: failed_at=%v error_code=%q", row.FailedAt.Valid, row.ErrorCode)
	}
}

func TestSandboxGenerationRejectsStaleOperationsBeforeReplacement(t *testing.T) {
	db := dbtest.New(t)
	q := sqlc.New(db)
	createSession(t, q, "session-sandbox")
	store := newTestStore(t, db)
	run, err := store.Acquire(t.Context(), "session-sandbox", "web")
	if err != nil {
		t.Fatal(err)
	}
	firstLease, err := ReserveSandbox(run.Context(), "session-sandbox")
	if err != nil {
		t.Fatal(err)
	}
	first, err := firstLease.Activate(run.Context(), pkgsandbox.NopSession())
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	secondLease, err := ReserveSandbox(run.Context(), "session-sandbox")
	if err != nil {
		t.Fatal(err)
	}
	firstGeneration, err := q.GetSessionSandbox(t.Context(), "session-sandbox")
	if err != nil {
		t.Fatal(err)
	}
	if firstGeneration.Generation != 2 {
		t.Fatalf("replacement generation = %d, want 2", firstGeneration.Generation)
	}
	if _, err := first.Exec(run.Context(), "true", pkgsandbox.ExecOptions{}); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale sandbox Exec = %v, want ErrLeaseLost", err)
	}
	second, err := secondLease.Activate(run.Context(), pkgsandbox.NopSession())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.Exec(run.Context(), "true", pkgsandbox.ExecOptions{}); err != nil {
		t.Fatalf("current sandbox Exec: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	if err := run.Finish(t.Context(), StatusCompleted, ""); err != nil {
		t.Fatal(err)
	}
}

type recordingFiles struct{ writes atomic.Int64 }

func (*recordingFiles) ReadFile(string) ([]byte, error) { return nil, nil }
func (*recordingFiles) ReadDir(string) ([]pkgsandbox.DirEntry, error) {
	return nil, nil
}

func (*recordingFiles) Stat(string) (pkgsandbox.FileInfo, error) {
	return pkgsandbox.FileInfo{}, nil
}

func (f *recordingFiles) WriteFile(string, []byte, fs.FileMode) error {
	f.writes.Add(1)
	return nil
}
func (*recordingFiles) ProjectFiles(string, []pkgsandbox.ProjectedFile) error { return nil }
func (*recordingFiles) ProjectTempFiles(string, []pkgsandbox.ProjectedFile) (string, error) {
	return "/tmp/projected", nil
}

type recordingSandbox struct {
	pkgsandbox.Session
	files *recordingFiles
}

func (s recordingSandbox) Files() pkgsandbox.FileAccess { return s.files }

func TestSandboxFileCapabilityRejectsTerminalRunBeforeGenerationCleanup(t *testing.T) {
	db := dbtest.New(t)
	q := sqlc.New(db)
	createSession(t, q, "session-sandbox-files")
	store := newTestStore(t, db)
	firstRun, err := store.Acquire(t.Context(), "session-sandbox-files", "web")
	if err != nil {
		t.Fatal(err)
	}
	lease, err := ReserveSandbox(firstRun.Context(), "session-sandbox-files")
	if err != nil {
		t.Fatal(err)
	}
	innerFiles := &recordingFiles{}
	session, err := lease.Activate(firstRun.Context(), recordingSandbox{Session: pkgsandbox.NopSession(), files: innerFiles})
	if err != nil {
		t.Fatal(err)
	}
	files := session.Files()
	if err := firstRun.Finish(t.Context(), StatusCompleted, ""); err != nil {
		t.Fatal(err)
	}
	secondRun, err := store.Acquire(t.Context(), "session-sandbox-files", "web")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = secondRun.Finish(t.Context(), StatusCompleted, "") }()

	if err := files.WriteFile("stale", []byte("data"), 0o600); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale WriteFile error = %v, want ErrLeaseLost", err)
	}
	if got := innerFiles.writes.Load(); got != 0 {
		t.Fatalf("underlying writes = %d, want zero", got)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
}

type uncertainSandbox struct {
	pkgsandbox.Session
	closeFails atomic.Bool
}

type blockingCloseSandbox struct {
	pkgsandbox.Session
	started chan struct{}
	release chan struct{}
}

func (s *blockingCloseSandbox) Close() error {
	close(s.started)
	<-s.release
	return nil
}

func TestSandboxActivationFailureFencesBeforeProviderCleanup(t *testing.T) {
	db := dbtest.New(t)
	q := sqlc.New(db)
	createSession(t, q, "session-sandbox-activate-fence")
	store := newTestStore(t, db)
	run, err := store.Acquire(t.Context(), "session-sandbox-activate-fence", "web")
	if err != nil {
		t.Fatal(err)
	}
	lease, err := ReserveSandbox(run.Context(), "session-sandbox-activate-fence")
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Finish(t.Context(), StatusCompleted, ""); err != nil {
		t.Fatal(err)
	}
	inner := &blockingCloseSandbox{Session: pkgsandbox.NopSession(), started: make(chan struct{}), release: make(chan struct{})}
	activateDone := make(chan error, 1)
	go func() {
		_, err := lease.Activate(t.Context(), inner)
		activateDone <- err
	}()
	select {
	case <-inner.started:
	case <-time.After(5 * time.Second):
		t.Fatal("activation cleanup did not reach provider Close")
	}
	row, err := q.GetSessionSandbox(t.Context(), "session-sandbox-activate-fence")
	if err != nil {
		t.Fatal(err)
	}
	if row.State != "fenced" || row.DestroyedAt.Valid {
		t.Fatalf("state while provider Close blocked = %q destroyed=%v, want fenced/unproven", row.State, row.DestroyedAt.Valid)
	}
	close(inner.release)
	if err := <-activateDone; !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("Activate error = %v, want ErrLeaseLost", err)
	}
	row, err = q.GetSessionSandbox(t.Context(), "session-sandbox-activate-fence")
	if err != nil {
		t.Fatal(err)
	}
	if row.State != "destroyed" || !row.DestroyedAt.Valid {
		t.Fatalf("state after provider Close = %q destroyed=%v, want destroyed/proven", row.State, row.DestroyedAt.Valid)
	}
}

func (s *uncertainSandbox) Exec(context.Context, string, pkgsandbox.ExecOptions) (pkgsandbox.ExecResult, error) {
	return pkgsandbox.ExecResult{}, errors.New("provider outcome unknown")
}

func (s *uncertainSandbox) Close() error {
	if s.closeFails.Load() {
		return errors.New("provider cleanup unproven")
	}
	return nil
}

func TestSandboxOperationUncertaintyFencesUntilDestructionIsProven(t *testing.T) {
	db := dbtest.New(t)
	q := sqlc.New(db)
	createSession(t, q, "session-sandbox-uncertain")
	store := newTestStore(t, db)
	run, err := store.Acquire(t.Context(), "session-sandbox-uncertain", "web")
	if err != nil {
		t.Fatal(err)
	}
	lease, err := ReserveSandbox(run.Context(), "session-sandbox-uncertain")
	if err != nil {
		t.Fatal(err)
	}
	inner := &uncertainSandbox{Session: pkgsandbox.NopSession()}
	inner.closeFails.Store(true)
	session, err := lease.Activate(run.Context(), inner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.Exec(run.Context(), "unknown", pkgsandbox.ExecOptions{}); err == nil {
		t.Fatal("uncertain Exec returned nil error")
	}
	row, err := q.GetSessionSandbox(t.Context(), "session-sandbox-uncertain")
	if err != nil {
		t.Fatal(err)
	}
	if row.State != "fenced" || row.DestroyedAt.Valid {
		t.Fatalf("uncertain generation state = %q destroyed=%v, want fenced/unproven", row.State, row.DestroyedAt.Valid)
	}
	if _, err := ReserveSandbox(run.Context(), "session-sandbox-uncertain"); err == nil {
		t.Fatal("replacement started before old-resource destruction was proven")
	}

	inner.closeFails.Store(false)
	if err := session.Close(); err != nil {
		t.Fatalf("retry cleanup: %v", err)
	}
	replacement, err := ReserveSandbox(run.Context(), "session-sandbox-uncertain")
	if err != nil {
		t.Fatalf("reserve replacement after proven cleanup: %v", err)
	}
	row, err = q.GetSessionSandbox(t.Context(), "session-sandbox-uncertain")
	if err != nil {
		t.Fatal(err)
	}
	if row.Generation != 2 || row.State != "creating" {
		t.Fatalf("replacement generation/state = %d/%q, want 2/creating", row.Generation, row.State)
	}
	if err := replacement.Abandon(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := run.Finish(t.Context(), StatusCompleted, ""); err != nil {
		t.Fatal(err)
	}
}

func TestSandboxCrashRecoveryReconstructsCleanupBeforeReplacement(t *testing.T) {
	db := dbtest.New(t)
	q := sqlc.New(db)
	createSession(t, q, "session-sandbox-crash")
	oldBootID := NewBootID()
	if _, err := q.CreateExecutorBoot(t.Context(), sqlc.CreateExecutorBootParams{ID: oldBootID}); err != nil {
		t.Fatal(err)
	}
	oldStore := NewStore(db, oldBootID)
	runCtx, cancelRun := context.WithCancel(t.Context())
	oldRun, err := oldStore.Acquire(runCtx, "session-sandbox-crash", "web")
	if err != nil {
		t.Fatal(err)
	}
	lease, err := ReserveSandbox(oldRun.Context(), "session-sandbox-crash", "docker")
	if err != nil {
		t.Fatal(err)
	}
	row, err := q.GetSessionSandbox(t.Context(), "session-sandbox-crash")
	if err != nil {
		t.Fatal(err)
	}
	if row.State != "creating" || row.ResourceBackend != "docker" || row.ResourceID != lease.ResourceID() || !row.RunID.Valid || row.RunID.String != oldRun.Guard.RunID {
		t.Fatalf("pre-create durable resource = %q/%q/%q run=%#v, want creating/docker/%q run=%q", row.State, row.ResourceBackend, row.ResourceID, row.RunID, lease.ResourceID(), oldRun.Guard.RunID)
	}

	// Simulate process death in the external-create uncertainty window: the
	// deterministic provider identity is durable, but no activation write ran.
	cancelRun()
	if _, err := db.Exec(t.Context(), `UPDATE agent_run SET lease_expires_at = now() - interval '1 second' WHERE id = $1`, oldRun.Guard.RunID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(t.Context(), `UPDATE runtime_executor_boot SET heartbeat_at = now() - interval '1 minute' WHERE id = $1`, oldBootID); err != nil {
		t.Fatal(err)
	}
	cleanupFails := true
	var cleanedBackend, cleanedID string
	newStore := newTestStore(t, db, func(_ context.Context, backend, resourceID string) error {
		cleanedBackend, cleanedID = backend, resourceID
		if cleanupFails {
			return errors.New("provider cleanup unavailable")
		}
		return nil
	})
	if err := newStore.Reap(t.Context()); err == nil {
		t.Fatal("recovery reported success while resource destruction was unproven")
	}
	row, err = q.GetSessionSandbox(t.Context(), "session-sandbox-crash")
	if err != nil {
		t.Fatal(err)
	}
	if row.State != "fenced" || row.DestroyedAt.Valid {
		t.Fatalf("failed recovery state = %q destroyed=%v, want fenced/unproven", row.State, row.DestroyedAt.Valid)
	}
	if cleanedBackend != "docker" || cleanedID != lease.ResourceID() {
		t.Fatalf("reconstructed cleanup = %q/%q, want docker/%q", cleanedBackend, cleanedID, lease.ResourceID())
	}
	replacementRun, err := newStore.Acquire(t.Context(), "session-sandbox-crash", "web")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ReserveSandbox(replacementRun.Context(), "session-sandbox-crash", "docker"); err == nil {
		t.Fatal("replacement reserved before destruction was proven")
	}

	cleanupFails = false
	if err := newStore.Reap(t.Context()); err != nil {
		t.Fatalf("retry recovery: %v", err)
	}
	row, err = q.GetSessionSandbox(t.Context(), "session-sandbox-crash")
	if err != nil {
		t.Fatal(err)
	}
	if row.State != "destroyed" || !row.DestroyedAt.Valid {
		t.Fatalf("recovered state = %q destroyed=%v, want destroyed", row.State, row.DestroyedAt.Valid)
	}

	replacement, err := ReserveSandbox(replacementRun.Context(), "session-sandbox-crash", "docker")
	if err != nil {
		t.Fatalf("reserve replacement: %v", err)
	}
	row, err = q.GetSessionSandbox(t.Context(), "session-sandbox-crash")
	if err != nil {
		t.Fatal(err)
	}
	if row.Generation != 2 || row.ResourceID != replacement.ResourceID() || row.ResourceID == lease.ResourceID() {
		t.Fatalf("replacement generation/resource = %d/%q, previous %q", row.Generation, row.ResourceID, lease.ResourceID())
	}
	if err := replacement.Abandon(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := replacementRun.Finish(t.Context(), StatusCompleted, ""); err != nil {
		t.Fatal(err)
	}
}

func TestSandboxCreateUncertaintyRequiresReconstructedCleanup(t *testing.T) {
	db := dbtest.New(t)
	q := sqlc.New(db)
	createSession(t, q, "session-sandbox-create-unknown")
	cleanupFails := true
	var cleanedBackend, cleanedID string
	store := newTestStore(t, db, func(_ context.Context, backend, resourceID string) error {
		cleanedBackend, cleanedID = backend, resourceID
		if cleanupFails {
			return errors.New("provider cleanup outcome unknown")
		}
		return nil
	})
	run, err := store.Acquire(t.Context(), "session-sandbox-create-unknown", "web")
	if err != nil {
		t.Fatal(err)
	}
	lease, err := ReserveSandbox(run.Context(), "session-sandbox-create-unknown", "docker")
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.CleanupCreationFailure(t.Context()); err == nil {
		t.Fatal("outcome-unknown provider cleanup unexpectedly proved absence")
	}
	row, err := q.GetSessionSandbox(t.Context(), "session-sandbox-create-unknown")
	if err != nil {
		t.Fatal(err)
	}
	if row.State != "fenced" || row.DestroyedAt.Valid {
		t.Fatalf("failed create cleanup state = %q destroyed=%v, want fenced/unproven", row.State, row.DestroyedAt.Valid)
	}
	if cleanedBackend != "docker" || cleanedID != lease.ResourceID() {
		t.Fatalf("reconstructed cleanup = %q/%q, want docker/%q", cleanedBackend, cleanedID, lease.ResourceID())
	}
	if _, err := ReserveSandbox(run.Context(), "session-sandbox-create-unknown", "docker"); err == nil {
		t.Fatal("replacement reserved while create outcome remained unknown")
	}

	cleanupFails = false
	if err := lease.CleanupCreationFailure(t.Context()); err != nil {
		t.Fatalf("retry reconstructed cleanup: %v", err)
	}
	row, err = q.GetSessionSandbox(t.Context(), "session-sandbox-create-unknown")
	if err != nil {
		t.Fatal(err)
	}
	if row.State != "destroyed" || !row.DestroyedAt.Valid {
		t.Fatalf("successful create cleanup state = %q destroyed=%v, want destroyed/proven", row.State, row.DestroyedAt.Valid)
	}
	replacement, err := ReserveSandbox(run.Context(), "session-sandbox-create-unknown", "docker")
	if err != nil {
		t.Fatalf("reserve after reconstructed cleanup: %v", err)
	}
	if err := replacement.Abandon(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := run.Finish(t.Context(), StatusCompleted, ""); err != nil {
		t.Fatal(err)
	}
}
