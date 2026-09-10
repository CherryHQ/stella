// Package agentrun owns the PostgreSQL execution lease shared by every Agent
// entry path. A process-local runtime gate may reject faster, but this package
// is the cross-replica authority.
//
// The lease covers execution only: admission, heartbeat, cancellation, expiry
// recovery, and the terminal transition, which the party that commits the last
// required durable result performs. External delivery is the channel's concern
// and deliberately has no state here — a Run never stays alive because a
// platform has not acknowledged bytes.
package agentrun

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

var (
	ErrBusy            = errors.New("agent run already active")
	ErrLeaseLost       = errors.New("agent run ownership lost")
	ErrInvalidGuard    = errors.New("agent run guard is invalid")
	ErrStoreClosed     = errors.New("agent run store is closed")
	ErrBootUnavailable = errors.New("agent run executor boot is not running")
)

// terminalWriteTimeout bounds a terminal write so a canceled caller context or a
// broken database cannot leave a Run unsettled for its whole lease.
const terminalWriteTimeout = 5 * time.Second

const (
	StatusCompleted   = "completed"
	StatusFailed      = "failed"
	StatusCanceled    = "canceled"
	StatusAborted     = "aborted"
	StatusInterrupted = "interrupted"
	defaultLease      = 30 * time.Second
)

// Guard is immutable proof of one Run owner. Durable writers carry it through
// context and validate it in the transaction that commits their write.
type Guard struct {
	RunID          string
	SessionID      string
	ExecutorBootID string
}

// InboxAdmissionWriter composes the canonical transcript projection with
// AgentRun admission. The callback runs in the same transaction after the
// inbox row is linked to the newly created Run, so a committed link can never
// exist without its input projection. It receives the immutable target Guard
// for diagnostics and future guarded writers; the callback owns no commit.
type InboxAdmissionWriter func(context.Context, pgx.Tx, Guard) error

type (
	guardKey       struct{}
	guardedContext struct {
		Guard
		store *Store
	}
)

// WithGuard attaches an immutable guard. It intentionally does not attach a
// Store, so callers cannot accidentally claim operation-check authority from a
// bare identifier; Lease.Context is the production source of a live guard.
func WithGuard(ctx context.Context, guard Guard) context.Context {
	return context.WithValue(ctx, guardKey{}, guardedContext{Guard: guard})
}

func GuardFromContext(ctx context.Context) (Guard, bool) {
	value, ok := ctx.Value(guardKey{}).(guardedContext)
	// The boolean reports that the caller supplied an ownership context, even
	// when its fields are malformed. Callers must route that case through
	// ValidateTx/Check so an invalid guard cannot silently downgrade to an
	// unguarded management write.
	return value.Guard, ok
}

func withLeaseGuard(ctx context.Context, guard Guard, store *Store) context.Context {
	return context.WithValue(ctx, guardKey{}, guardedContext{Guard: guard, store: store})
}

// Check is the fail-closed model/tool/Sandbox operation fence. Durable writes
// use ValidateTx because the check must be coupled to their commit.
func Check(ctx context.Context) error {
	value, ok := ctx.Value(guardKey{}).(guardedContext)
	if !ok {
		return nil
	}
	if !guardValid(value.Guard) || value.store == nil {
		return ErrInvalidGuard
	}
	if value.store.closed.Load() || value.store.lifecycleCtx.Err() != nil {
		return ErrLeaseLost
	}
	_, err := value.store.q.LockAgentRunOwnership(ctx, sqlc.LockAgentRunOwnershipParams{
		RunID: value.RunID, SessionID: value.SessionID, ExecutorBootID: value.ExecutorBootID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrLeaseLost
	}
	if err != nil {
		return fmt.Errorf("check AgentRun ownership: %w", err)
	}
	return nil
}

// ValidateTx takes a row lock compatible with operation readers but conflicting
// with terminal updates. The ownership check and caller's write therefore
// commit in one serialization order.
func ValidateTx(ctx context.Context, tx pgx.Tx) error {
	value, hasGuard := ctx.Value(guardKey{}).(guardedContext)
	if !hasGuard {
		return nil
	}
	if !guardValid(value.Guard) {
		return ErrInvalidGuard
	}
	if value.store != nil && (value.store.closed.Load() || value.store.lifecycleCtx.Err() != nil) {
		return ErrLeaseLost
	}
	guard := value.Guard
	_, err := sqlc.New(tx).LockAgentRunOwnership(ctx, sqlc.LockAgentRunOwnershipParams{
		RunID: guard.RunID, SessionID: guard.SessionID, ExecutorBootID: guard.ExecutorBootID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrLeaseLost
	}
	if err != nil {
		return fmt.Errorf("validate AgentRun ownership: %w", err)
	}
	return nil
}

type Store struct {
	db              *pgxpool.Pool
	q               *sqlc.Queries
	executorBootID  string
	lease           time.Duration
	lifecycleCtx    context.Context
	lifecycleCancel context.CancelCauseFunc
	closed          atomic.Bool
	bootRegistered  atomic.Bool
	bootDrained     atomic.Bool
	admissionMu     sync.RWMutex
	mu              sync.Mutex
	local           map[string]*Lease
}

// leaseContext keeps request values available to the runtime while taking
// cancellation and deadlines exclusively from the Store lifecycle. A child
// turn context can therefore stop model work without stopping the ownership
// heartbeat that protects the final source commit.
type leaseContext struct {
	values    context.Context
	lifecycle context.Context
}

func (c leaseContext) Deadline() (time.Time, bool) { return c.lifecycle.Deadline() }
func (c leaseContext) Done() <-chan struct{}       { return c.lifecycle.Done() }
func (c leaseContext) Err() error                  { return c.lifecycle.Err() }
func (c leaseContext) Value(key any) any           { return c.values.Value(key) }

func NewStore(db *pgxpool.Pool, executorBootID string) *Store {
	return NewStoreWithContext(context.Background(), db, executorBootID)
}

// NewStoreWithContext binds lease heartbeats to the process/runtime lifecycle,
// rather than to an individual model turn. The owner should cancel this
// context during process shutdown, after draining new admissions.
func NewStoreWithContext(parent context.Context, db *pgxpool.Pool, executorBootID string) *Store {
	return NewStoreWithLease(parent, db, executorBootID, defaultLease)
}

// NewStoreWithLease is NewStoreWithContext with an explicit lease duration.
// Deployments normally use the default; a shorter value is useful for an
// integration test that needs to cross a heartbeat tick without waiting 30s.
func NewStoreWithLease(parent context.Context, db *pgxpool.Pool, executorBootID string, lease time.Duration) *Store {
	if parent == nil {
		parent = context.Background()
	}
	if lease <= 0 {
		lease = defaultLease
	}
	lifecycleCtx, lifecycleCancel := context.WithCancelCause(parent)
	return &Store{
		db: db, q: sqlc.New(db), executorBootID: executorBootID,
		lease: lease, lifecycleCtx: lifecycleCtx, lifecycleCancel: lifecycleCancel,
		local: make(map[string]*Lease),
	}
}

// Close stops local heartbeat workers. Durable terminalization remains the
// reaper's job, so a process that exits after Close cannot leave a replacement
// executor believing the old owner is still alive.
func (s *Store) Close() {
	if s == nil {
		return
	}
	// Mark shutdown before taking the admission lock. An admission already in
	// flight holds the read side and sees the lifecycle cancellation through its
	// bound context; admissions that have not started fail closed immediately.
	s.closed.Store(true)
	if s.lifecycleCancel != nil {
		s.lifecycleCancel(ErrLeaseLost)
	}
	s.admissionMu.Lock()
	defer s.admissionMu.Unlock()
	s.mu.Lock()
	leases := make([]*Lease, 0, len(s.local))
	for _, lease := range s.local {
		leases = append(leases, lease)
	}
	s.local = make(map[string]*Lease)
	s.mu.Unlock()
	for _, lease := range leases {
		lease.cancel(ErrLeaseLost)
	}
}

func (s *Store) leaseSeconds() int32 {
	if s == nil || s.lease <= 0 {
		return 1
	}
	seconds := int32((s.lease + time.Second - 1) / time.Second)
	if seconds < 1 {
		return 1
	}
	return seconds
}

func NewBootID() string { return uuid.Must(uuid.NewV7()).String() }

func (s *Store) ExecutorBootID() string {
	if s == nil {
		return ""
	}
	return s.executorBootID
}

// Lease is one executor's renewable ownership of a Session.
type Lease struct {
	Guard Guard
	ctx   context.Context
	// cancel is kept private so adapter code can only stop through the
	// cancellation paths the runtime owns.
	cancel context.CancelCauseFunc
	store  *Store

	heartbeatDone chan struct{}
}

func (l *Lease) Context() context.Context { return withLeaseGuard(l.ctx, l.Guard, l.store) }

// ContextWith binds this Lease's target guard onto an existing execution
// context. It deliberately preserves the caller's values, deadline, and
// cancellation; Lease.Context is the Store-lifetime context for heartbeat
// ownership, while this method is the normal runtime/admission bridge.
func (l *Lease) ContextWith(ctx context.Context) context.Context {
	return withLeaseGuard(ctx, l.Guard, l.store)
}

// Acquire creates a Run after interrupting an already expired owner.
func (s *Store) Acquire(ctx context.Context, sessionID, source string) (*Lease, error) {
	return s.acquire(ctx, sessionID, source, "")
}

// AcquireForInbox atomically links a pending Session inbox receipt, its
// canonical transcript projection, and the new Run. The writer is required so
// a linked receipt can never become a model turn without its input projection.
func (s *Store) AcquireForInbox(ctx context.Context, sessionID, source, inboxID string, write InboxAdmissionWriter) (*Lease, error) {
	if inboxID == "" {
		return nil, errors.New("session inbox ID is required")
	}
	if write == nil {
		return nil, errors.New("session inbox admission writer is required")
	}
	return s.acquireWithWriter(ctx, sessionID, source, inboxID, write)
}

func (s *Store) acquire(ctx context.Context, sessionID, source, inboxID string) (*Lease, error) {
	return s.acquireWithWriter(ctx, sessionID, source, inboxID, nil)
}

func (s *Store) acquireWithWriter(ctx context.Context, sessionID, source, inboxID string, write InboxAdmissionWriter) (*Lease, error) {
	if s == nil || s.db == nil || s.q == nil || s.executorBootID == "" {
		return nil, errors.New("AgentRun store is not configured")
	}
	if sessionID == "" || source == "" {
		return nil, errors.New("AgentRun session and source are required")
	}
	if inboxID != "" && write == nil {
		return nil, errors.New("session inbox admission writer is required")
	}
	if err := s.requireAdmission(); err != nil {
		return nil, err
	}
	if !s.bootRegistered.Load() || s.bootDrained.Load() {
		return nil, ErrBootUnavailable
	}
	// Serialize shutdown against the commit point. The lifecycle-bound child
	// context makes Close interrupt an in-flight DB call while the read lock
	// prevents Close from racing a successful commit into a post-shutdown lease.
	s.admissionMu.RLock()
	defer s.admissionMu.RUnlock()
	if err := s.requireAdmission(); err != nil {
		return nil, err
	}
	if !s.bootRegistered.Load() || s.bootDrained.Load() {
		return nil, ErrBootUnavailable
	}
	admissionCtx, release := s.bindLifecycle(ctx)
	defer release()
	tx, err := s.db.Begin(admissionCtx)
	if err != nil {
		if s.closed.Load() {
			return nil, ErrStoreClosed
		}
		return nil, fmt.Errorf("begin AgentRun admission: %w", err)
	}
	defer func() { _ = tx.Rollback(admissionCtx) }()
	qtx := s.q.WithTx(tx)
	if _, err := qtx.LockRunningExecutorBoot(admissionCtx, s.executorBootID); errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrBootUnavailable
	} else if err != nil {
		if s.closed.Load() {
			return nil, ErrStoreClosed
		}
		return nil, fmt.Errorf("lock AgentRun executor boot: %w", err)
	}
	if _, err := qtx.InterruptExpiredAgentRunBySession(admissionCtx, sessionID); err != nil {
		if s.closed.Load() {
			return nil, ErrStoreClosed
		}
		return nil, fmt.Errorf("terminalize expired AgentRun: %w", err)
	}
	id := uuid.Must(uuid.NewV7()).String()
	row, err := qtx.CreateAgentRun(admissionCtx, sqlc.CreateAgentRunParams{
		ID: id, SessionID: sessionID, ExecutorBootID: s.executorBootID,
		Source: source, LeaseSeconds: s.leaseSeconds(),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrBusy
	}
	if err != nil {
		if s.closed.Load() {
			return nil, ErrStoreClosed
		}
		return nil, fmt.Errorf("create AgentRun: %w", err)
	}
	if inboxID != "" {
		// The source Run guard is still on ctx at this boundary. Lock it in this
		// transaction before creating and linking the target Run, so a source
		// abort cannot race an accepted Session.send receipt.
		if err := ValidateTx(admissionCtx, tx); err != nil {
			return nil, fmt.Errorf("validate source AgentRun: %w", err)
		}
		rows, err := qtx.LinkSessionInboxRun(admissionCtx, sqlc.LinkSessionInboxRunParams{
			RunID: row.ID, ID: inboxID, TargetSessionID: row.SessionID,
		})
		if err != nil {
			return nil, fmt.Errorf("link Session inbox AgentRun: %w", err)
		}
		if rows != 1 {
			return nil, errors.New("session inbox is no longer pending or already linked")
		}
		if err := write(admissionCtx, tx, Guard{RunID: row.ID, SessionID: row.SessionID, ExecutorBootID: row.ExecutorBootID}); err != nil {
			return nil, fmt.Errorf("write Session inbox admission: %w", err)
		}
	}
	if err := tx.Commit(admissionCtx); err != nil {
		if s.closed.Load() {
			return nil, ErrStoreClosed
		}
		return nil, fmt.Errorf("commit AgentRun admission: %w", err)
	}
	if err := s.requireAdmission(); err != nil {
		// Close cannot race this check with a successful commit while holding the
		// admission read lock. Keep the explicit check as a guard against a
		// future caller bypassing the lock around a new admission path.
		return nil, err
	}

	// The lease context survives a caller's turn cancellation: the party that
	// commits this Run's final result needs it to terminalize after ordinary
	// model cancellation. Callers derive their own turn context when they need
	// cancellable model work.
	runCtx, cancel := context.WithCancelCause(leaseContext{values: ctx, lifecycle: s.lifecycleCtx})
	lease := &Lease{
		Guard:         Guard{RunID: row.ID, SessionID: row.SessionID, ExecutorBootID: row.ExecutorBootID},
		ctx:           runCtx,
		cancel:        cancel,
		store:         s,
		heartbeatDone: make(chan struct{}),
	}
	s.mu.Lock()
	s.local[row.ID] = lease
	s.mu.Unlock()
	go lease.heartbeat()
	return lease, nil
}

func (s *Store) requireAdmission() error {
	if s == nil || s.closed.Load() || s.lifecycleCtx == nil || s.lifecycleCtx.Err() != nil {
		return ErrStoreClosed
	}
	return nil
}

// bindLifecycle preserves caller values and cancellation while adding the
// Store lifetime as a second cancellation source. It is deliberately used for
// admission and terminal writes, but Lease.ContextWith remains the caller's
// turn context so model cancellation does not stop the heartbeat.
func (s *Store) bindLifecycle(ctx context.Context) (context.Context, func()) {
	if ctx == nil {
		ctx = context.Background()
	}
	bound, cancel := context.WithCancelCause(ctx)
	stop := context.AfterFunc(s.lifecycleCtx, func() {
		cancel(context.Cause(s.lifecycleCtx))
	})
	return bound, func() {
		stop()
		cancel(nil)
	}
}

func (l *Lease) heartbeat() {
	interval := l.store.lease / 3
	if interval <= 0 {
		interval = time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	defer close(l.heartbeatDone)
	for {
		select {
		case <-l.ctx.Done():
			return
		case <-ticker.C:
			// A blocked UPDATE must not outlive the lease safety window. Without
			// this bound a DB partition could leave model/tool work running on an
			// owner whose durable lease has already expired.
			heartbeatCtx, cancel := context.WithTimeout(l.ctx, interval)
			_, err := l.store.q.HeartbeatAgentRun(heartbeatCtx, sqlc.HeartbeatAgentRunParams{
				RunID: l.Guard.RunID, ExecutorBootID: l.Guard.ExecutorBootID,
				LeaseSeconds: l.store.leaseSeconds(),
			})
			cancel()
			if err != nil {
				l.cancel(ErrLeaseLost)
				return
			}
		}
	}
}

func (l *Lease) stopHeartbeat(cause error) {
	l.cancel(cause)
	<-l.heartbeatDone
	l.store.mu.Lock()
	delete(l.store.local, l.Guard.RunID)
	l.store.mu.Unlock()
}

// Finish commits the terminal transition of this Run in its own transaction. Use
// it when the required durable result is already committed and needs no atomic
// coupling; use FinishWith when the two must commit together.
func (l *Lease) Finish(ctx context.Context, status, reason string) error {
	return l.FinishWith(ctx, status, reason, nil)
}

// FinishWith commits a caller's durable handoff and this Run's terminal
// transition in one transaction owned by this method. The callback runs after
// the terminal statement and receives the status PostgreSQL actually wrote, so a
// durable stop request that outranked the caller's status also decides whether
// the handoff may be published.
//
// Local lifecycle changes only after the commit: a rolled-back or failed
// transaction leaves the Run running and this owner still renewing it. There is
// deliberately no claim flag and no acknowledgement handshake — the row decides
// the winner, and the heartbeat plus reconciliation observe that decision.
//
// Callers must pass a context that does not inherit a canceled turn: a stopped
// turn still has to be able to record its outcome.
func (l *Lease) FinishWith(ctx context.Context, status, reason string, before func(context.Context, pgx.Tx, string) error) error {
	if l == nil || l.store == nil {
		return errors.New("AgentRun lease is not configured")
	}
	l.store.admissionMu.RLock()
	defer l.store.admissionMu.RUnlock()
	if err := l.store.requireAdmission(); err != nil {
		l.Release()
		return errors.Join(ErrLeaseLost, err)
	}
	if !validStatus(status) {
		return fmt.Errorf("invalid AgentRun completion status %q", status)
	}
	// A lost lease and a closing store both mean this Run is no longer renewable
	// here; recovery owns it. A caller cancellation (an explicit stop) does not:
	// the owner must still be able to record the stopped turn's result.
	if cause := context.Cause(l.ctx); errors.Is(cause, ErrLeaseLost) {
		l.Release()
		return ErrLeaseLost
	}
	// The write is bounded by its own timeout rather than the caller's deadline,
	// because a stopped turn still has to record its outcome.
	opCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), terminalWriteTimeout)
	defer cancel()
	opCtx, stopLifecycle := l.store.bindLifecycle(opCtx)
	defer stopLifecycle()
	tx, err := l.store.db.Begin(opCtx)
	if err != nil {
		return fmt.Errorf("begin AgentRun terminal transition: %w", err)
	}
	defer func() { _ = tx.Rollback(opCtx) }()
	written, err := TerminalTx(opCtx, tx, l.Guard, status, reason)
	if errors.Is(err, ErrLeaseLost) {
		// The row is not this owner's to finish any more: another decision (abort,
		// expiry, a replacement boot) already won. Stop renewing it and report the
		// same conflict the caller would see from a duplicate call.
		l.Release()
		return err
	}
	if err != nil {
		return err
	}
	if before != nil {
		if err := before(opCtx, tx, written); err != nil {
			// The caller's own durable handoff did not commit, so this Run must not
			// be terminalized here. Leaving it running lets the caller record the
			// failure through the ordinary path or lets expiry recover it.
			return err
		}
	}
	if err := l.store.requireAdmission(); err != nil {
		return err
	}
	if err := tx.Commit(opCtx); err != nil {
		return fmt.Errorf("commit AgentRun terminal transition: %w", err)
	}
	// Only now is the decision durable; release the local renewal worker.
	l.Release()
	return nil
}

// TerminalTx runs the single execution terminal statement on a caller-owned
// transaction and returns the status that was actually written, which may be
// 'aborted' when a durable stop request outranked the caller's status. It
// deliberately changes no local lifecycle: the caller commits, and then calls
// Release (or lets reconciliation observe the committed row).
func TerminalTx(ctx context.Context, tx pgx.Tx, guard Guard, status, reason string) (string, error) {
	if tx == nil {
		return "", errors.New("AgentRun terminal transaction is required")
	}
	if !validStatus(status) {
		return "", fmt.Errorf("invalid AgentRun completion status %q", status)
	}
	if !guardValid(guard) {
		return "", ErrInvalidGuard
	}
	if value, ok := ctx.Value(guardKey{}).(guardedContext); ok && value.store != nil &&
		(value.store.closed.Load() || value.store.lifecycleCtx.Err() != nil) {
		return "", ErrLeaseLost
	}
	row, err := sqlc.New(tx).CompleteAgentRunWithActivity(ctx, sqlc.CompleteAgentRunWithActivityParams{
		RunID: guard.RunID, SessionID: guard.SessionID, ExecutorBootID: guard.ExecutorBootID, Status: status, Reason: reason,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrLeaseLost
	}
	if err != nil {
		return "", fmt.Errorf("complete AgentRun: %w", err)
	}
	return row.Status, nil
}

// Release stops local renewal after the owner's final write attempt. If the
// database could not record a terminal result, expiry recovers the abandoned
// execution. It is idempotent and safe to defer at the owning call site.
func (l *Lease) Release() {
	if l == nil || l.store == nil {
		return
	}
	l.stopHeartbeat(nil)
}

func validStatus(status string) bool {
	switch status {
	case StatusCompleted, StatusFailed, StatusCanceled, StatusAborted, StatusInterrupted:
		return true
	default:
		return false
	}
}

func guardValid(guard Guard) bool {
	return guard.RunID != "" && guard.SessionID != "" && guard.ExecutorBootID != ""
}

// RequestAbort records durable stop intent and cancels local work. It is the
// only way to ask for a stop: the owner still performs the terminal transition,
// so the reason and the result come from one serialization order.
func (s *Store) RequestAbort(ctx context.Context, sessionID, reason string) (string, error) {
	if reason == "" {
		reason = "abort_requested"
	}
	row, err := s.q.RequestSessionAgentRunAbort(ctx, sqlc.RequestSessionAgentRunAbortParams{SessionID: sessionID, Reason: reason})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("request AgentRun abort: %w", err)
	}
	s.mu.Lock()
	lease := s.local[row.ID]
	s.mu.Unlock()
	if lease != nil {
		lease.cancel(context.Canceled)
	}
	return row.ID, nil
}

func (s *Store) Running(ctx context.Context, sessionID string) (sqlc.AgentRun, bool, error) {
	row, err := s.q.GetRunningAgentRunBySession(ctx, sessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return sqlc.AgentRun{}, false, nil
	}
	return row, err == nil, err
}

// Reap terminalizes expired or explicitly aborted Runs. It never authorizes a
// replacement to continue the old execution; the next Acquire starts fresh.
func (s *Store) Reap(ctx context.Context) error {
	if s == nil || s.db == nil || s.q == nil {
		return errors.New("AgentRun store is not configured")
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin AgentRun reap: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	expired, err := qtx.ReapExpiredAgentRun(ctx, 100)
	if err != nil {
		return fmt.Errorf("reap expired AgentRun: %w", err)
	}
	aborted, err := qtx.ReapAbortRequestedAgentRun(ctx, 100)
	if err != nil {
		return fmt.Errorf("reap aborted AgentRun: %w", err)
	}
	if _, err := qtx.TerminalizeLinkedSessionInbox(ctx); err != nil {
		return fmt.Errorf("terminalize linked Session inbox: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit AgentRun reap: %w", err)
	}
	for _, row := range expired {
		s.reapLease(row.ID)
	}
	for _, row := range aborted {
		s.reapLease(row.ID)
	}
	s.reconcileLocalLeases(ctx)
	return nil
}

// reconcileLocalLeases stops local work whose Run another executor already
// terminalized. Reap's terminalization queries only return rows this invocation
// changed, so a local lease may already be terminal in PostgreSQL while its
// heartbeat worker is merely still running. Query failures and still-running
// rows remain live: a transient database failure must not invent a terminal
// outcome.
func (s *Store) reconcileLocalLeases(ctx context.Context) {
	s.mu.Lock()
	leases := make([]*Lease, 0, len(s.local))
	for _, lease := range s.local {
		leases = append(leases, lease)
	}
	s.mu.Unlock()
	// One read per local Run keeps reconciliation bounded by local lease count;
	// batch this lookup if a large executor approaches the Reap deadline.
	for _, lease := range leases {
		row, err := s.q.GetAgentRun(ctx, lease.Guard.RunID)
		if err != nil || row.Status == "running" {
			continue
		}
		s.reapLease(lease.Guard.RunID)
	}
}

func (s *Store) reapLease(id string) {
	s.mu.Lock()
	lease := s.local[id]
	delete(s.local, id)
	s.mu.Unlock()
	if lease != nil {
		lease.cancel(ErrLeaseLost)
	}
}

// LocalRuns reports the Runs this process still renews. It exists so tests and
// diagnostics can prove that a settled Run stops being renewed; nothing in the
// ownership rules depends on it.
func (s *Store) LocalRuns() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.local)
}
