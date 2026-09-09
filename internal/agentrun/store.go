// Package agentrun owns the PostgreSQL execution lease shared by every Agent
// entry path. A process-local runtime gate may reject faster, but this package
// is the cross-replica authority.
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
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
	"github.com/CherryHQ/stella/pkg/runcontrol"
)

var (
	ErrBusy               = errors.New("agent run already active")
	ErrLeaseLost          = errors.New("agent run ownership lost")
	ErrOutcomeUnknown     = errors.New("agent run completion outcome unknown")
	ErrCompletionConflict = errors.New("agent run completion outcome conflicts with an existing terminal result")
	ErrInvalidGuard       = errors.New("agent run guard is invalid")
	ErrStoreClosed        = errors.New("agent run store is closed")
	ErrBootUnavailable    = errors.New("agent run executor boot is not running")
)

const (
	StatusCompleted   = "completed"
	StatusFailed      = "failed"
	StatusCanceled    = "canceled"
	StatusAborted     = "aborted"
	StatusInterrupted = "interrupted"
	defaultLease      = 30 * time.Second
)

// turnResult is the durable Session activity result written in the same
// transaction as a winning AgentRun terminal transition. Keep this type local
// to agentrun to avoid importing memory back into the lease package.
type turnResult string

const (
	turnResultSuccess  turnResult = "success"
	turnResultError    turnResult = "error"
	turnResultCanceled turnResult = "canceled"
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

// InheritGuard copies the complete ownership fence, including its live Store
// checker, onto an adapter-owned context. Use this only after the target Run
// has been acquired, never to inherit source-session ownership.
func InheritGuard(ctx, source context.Context) (context.Context, bool) {
	value, ok := source.Value(guardKey{}).(guardedContext)
	if !ok || !guardValid(value.Guard) {
		return ctx, false
	}
	return context.WithValue(ctx, guardKey{}, value), true
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
// heartbeat that protects the final adapter acknowledgement.
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
	// cancel is kept private so adapter code can only stop through the explicit
	// abort path or context cancellation owned by the runtime.
	cancel context.CancelCauseFunc
	store  *Store

	heartbeatDone   chan struct{}
	terminal        chan struct{}
	terminalOnce    sync.Once
	transitionMu    sync.Mutex
	finished        bool
	finishedOutcome runcontrol.Outcome
	finishedStatus  string
	finishedReason  string
}

func (l *Lease) Context() context.Context { return withLeaseGuard(l.ctx, l.Guard, l.store) }

// Completion exposes the adapter-facing durable barrier. It is bound to this
// Lease, so Check and Ack cannot be redirected to another Session Run.
func (l *Lease) Completion() runcontrol.Completion {
	if l == nil {
		return nil
	}
	return leaseCompletion{lease: l}
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
	if link, _ := admissionCtx.Value(admissionLinkKey{}).(func(context.Context, pgx.Tx, Guard) error); link != nil {
		if err := link(admissionCtx, tx, Guard{RunID: row.ID, SessionID: row.SessionID, ExecutorBootID: row.ExecutorBootID}); err != nil {
			return nil, fmt.Errorf("link AgentRun source admission: %w", err)
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

	// A model turn may be canceled after it has emitted its final event while a
	// channel adapter is still delivering that event. Keep this context alive
	// until the durable terminal transition or Store.Close; callers derive a
	// separate turn context from Lease.Context when they need cancellation.
	runCtx, cancel := context.WithCancelCause(leaseContext{values: withoutAdmissionLink(ctx), lifecycle: s.lifecycleCtx})
	lease := &Lease{
		Guard: Guard{RunID: row.ID, SessionID: row.SessionID, ExecutorBootID: row.ExecutorBootID},
		ctx:   runCtx, cancel: cancel, store: s,
		heartbeatDone: make(chan struct{}), terminal: make(chan struct{}),
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

// operationContext preserves the caller's values and cancellation while also
// stopping terminal writes when this lease loses ownership or its Store is
// closed. A canceled model turn does not reach l.ctx, so it remains safe for
// adapters to acknowledge a final event after ordinary turn cancellation.
func (l *Lease) operationContext(ctx context.Context) (context.Context, func()) {
	if ctx == nil {
		ctx = context.Background()
	}
	bound, cancel := context.WithCancelCause(ctx)
	stop := context.AfterFunc(l.ctx, func() {
		cancel(context.Cause(l.ctx))
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

func (l *Lease) signalTerminal() { l.terminalOnce.Do(func() { close(l.terminal) }) }

// PrepareCompletion makes the final runtime result visible while retaining
// the lease for the adapter's external acknowledgement.
func (l *Lease) PrepareCompletion(ctx context.Context, status, reason string) error {
	if !validStatus(status) {
		return fmt.Errorf("invalid AgentRun completion status %q", status)
	}
	if l == nil || l.store == nil {
		return errors.New("AgentRun lease is not configured")
	}
	if err := l.leaseUnavailableError(ctx); err != nil {
		return err
	}
	opCtx, release := l.operationContext(ctx)
	defer release()
	rows, err := l.store.q.PrepareAgentRunCompletion(opCtx, sqlc.PrepareAgentRunCompletionParams{
		RunID: l.Guard.RunID, ExecutorBootID: l.Guard.ExecutorBootID, Status: status, Reason: reason,
	})
	if err != nil {
		return fmt.Errorf("prepare AgentRun completion: %w", err)
	}
	if rows != 1 {
		row, getErr := l.store.q.GetAgentRun(opCtx, l.Guard.RunID)
		if getErr == nil && row.Status == "running" && row.CompletionState == "ready" && row.CompletionStatus == status && row.CompletionReason == reason {
			return nil
		}
		return ErrLeaseLost
	}
	return nil
}

// leaseUnavailableError distinguishes a durable unknown terminal outcome from
// a plain lease loss. Local cancellation alone is not proof of either state:
// the reaper may have committed unknown just before canceling this Lease, or a
// heartbeat may have failed while the row is still running.
func (l *Lease) leaseUnavailableError(ctx context.Context) error {
	l.transitionMu.Lock()
	if l.finished {
		outcome := l.finishedOutcome
		l.transitionMu.Unlock()
		if outcome == runcontrol.OutcomeUnknown {
			return ErrOutcomeUnknown
		}
		return ErrLeaseLost
	}
	l.transitionMu.Unlock()
	if l.ctx.Err() == nil {
		return nil
	}
	row, err := l.store.q.GetAgentRun(ctx, l.Guard.RunID)
	if err != nil {
		return ErrLeaseLost
	}
	if row.Status != "running" && row.CompletionOutcome == string(runcontrol.OutcomeUnknown) {
		return ErrOutcomeUnknown
	}
	return ErrLeaseLost
}

// Finish directly terminalizes a Run whose external effect has no separate
// adapter acknowledgement. Adapter-backed callers should call PrepareCompletion
// and then Completion.Ack instead.
func (l *Lease) Finish(ctx context.Context, status, reason string) error {
	if l == nil || l.store == nil {
		return errors.New("AgentRun lease is not configured")
	}
	return l.finish(ctx, status, reason)
}

func (l *Lease) finish(ctx context.Context, status, reason string) error {
	if !validStatus(status) {
		return fmt.Errorf("invalid AgentRun completion status %q", status)
	}
	l.transitionMu.Lock()
	defer l.transitionMu.Unlock()
	if l.finished {
		if l.finishedOutcome == runcontrol.OutcomeUnknown {
			return ErrOutcomeUnknown
		}
		if l.finishedOutcome == runcontrol.OutcomeDelivered && l.finishedStatus == status && l.finishedReason == reason {
			return nil
		}
		return ErrCompletionConflict
	}
	if l.ctx.Err() != nil {
		return ErrLeaseLost
	}
	opCtx, release := l.operationContext(ctx)
	defer release()
	_, err := l.store.q.CompleteAgentRunWithActivity(opCtx, sqlc.CompleteAgentRunWithActivityParams{
		RunID: l.Guard.RunID, ExecutorBootID: l.Guard.ExecutorBootID,
		Status: status, Reason: reason, CompletionOutcome: string(runcontrol.OutcomeDelivered),
		TurnResult: pgtype.Text{String: string(turnResultForStatus(status)), Valid: true},
	})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("complete AgentRun: %w", err)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		row, getErr := l.store.q.GetAgentRun(opCtx, l.Guard.RunID)
		if getErr == nil && row.Status != "running" {
			return l.observeTerminalLocked(row.CompletionOutcome, row.CompletionStatus, row.CompletionReason, runcontrol.OutcomeDelivered, status, reason)
		}
		_, abortErr := l.store.q.AbortAgentRunWithActivity(opCtx, sqlc.AbortAgentRunWithActivityParams{RunID: l.Guard.RunID, ExecutorBootID: l.Guard.ExecutorBootID, Reason: "abort_requested"})
		if abortErr != nil {
			if errors.Is(abortErr, pgx.ErrNoRows) {
				return ErrLeaseLost
			}
			return fmt.Errorf("terminalize aborted AgentRun: %w", abortErr)
		}
		_ = l.rememberTerminalLocked(string(runcontrol.OutcomeDiscarded), StatusAborted, "abort_requested")
		return ErrLeaseLost
	}
	if err := l.rememberTerminalLocked(string(runcontrol.OutcomeDelivered), status, reason); err != nil {
		return err
	}
	if l.finishedOutcome == runcontrol.OutcomeUnknown {
		return ErrOutcomeUnknown
	}
	return nil
}

func (l *Lease) rememberTerminalLocked(raw, status, reason string) error {
	outcome := runcontrol.Outcome(raw)
	if !outcome.Valid() {
		if raw == "" {
			outcome = runcontrol.OutcomeUnknown
		} else {
			return ErrCompletionConflict
		}
	}
	l.finished = true
	l.finishedOutcome = outcome
	l.finishedStatus = status
	l.finishedReason = reason
	l.stopHeartbeat(nil)
	l.signalTerminal()
	return nil
}

func (l *Lease) observeTerminalLocked(raw, status, reason string, wantOutcome runcontrol.Outcome, wantStatus, wantReason string) error {
	if err := l.rememberTerminalLocked(raw, status, reason); err != nil {
		return err
	}
	if l.finishedOutcome == runcontrol.OutcomeUnknown {
		if wantOutcome == runcontrol.OutcomeUnknown {
			return nil
		}
		return ErrOutcomeUnknown
	}
	if l.finishedOutcome != wantOutcome || l.finishedStatus != wantStatus || l.finishedReason != wantReason {
		return ErrCompletionConflict
	}
	return nil
}

type leaseCompletion struct{ lease *Lease }

func (c leaseCompletion) Check(ctx context.Context) error {
	if c.lease == nil {
		return ErrLeaseLost
	}
	return Check(c.lease.ContextWith(ctx))
}

func (c leaseCompletion) Ack(ctx context.Context, outcome runcontrol.Outcome) error {
	if c.lease == nil {
		return ErrLeaseLost
	}
	if !outcome.Valid() {
		return runcontrol.ErrInvalidOutcome
	}
	return c.lease.ackWithActivity(ctx, outcome)
}

func (c leaseCompletion) Done() <-chan struct{} {
	if c.lease == nil {
		return nil
	}
	return c.lease.terminal
}

// ContextWith binds this Lease's target guard onto an existing execution
// context. It deliberately preserves the caller's values, deadline, and
// cancellation; Lease.Context is the Store-lifetime context for heartbeat
// ownership, while this method is the normal runtime/admission bridge.
func (l *Lease) ContextWith(ctx context.Context) context.Context {
	return withLeaseGuard(withoutAdmissionLink(ctx), l.Guard, l.store)
}

func (l *Lease) ackWithActivity(ctx context.Context, outcome runcontrol.Outcome) error {
	l.transitionMu.Lock()
	defer l.transitionMu.Unlock()
	if l.finished {
		if l.finishedOutcome == runcontrol.OutcomeUnknown {
			if outcome == runcontrol.OutcomeUnknown {
				return nil
			}
			return ErrOutcomeUnknown
		}
		if l.finishedOutcome == outcome {
			return nil
		}
		return ErrCompletionConflict
	}
	// Read the durable row with the caller's acknowledgement context first. A
	// reaper cancels the local heartbeat as soon as it records an unknown
	// outcome, but the adapter still needs to learn that durable terminal fact
	// instead of seeing only the local cancellation as ErrLeaseLost.
	row, err := l.store.q.GetAgentRun(ctx, l.Guard.RunID)
	if err != nil {
		return fmt.Errorf("read AgentRun completion before ack: %w", err)
	}
	if row.Status != "running" {
		return l.observeTerminalLocked(row.CompletionOutcome, row.CompletionStatus, row.CompletionReason, outcome, row.CompletionStatus, row.CompletionReason)
	}
	if l.ctx.Err() != nil {
		return ErrLeaseLost
	}
	opCtx, release := l.operationContext(ctx)
	defer release()
	status := completionStatus(row.CompletionStatus, outcome)
	result := turnResultForStatus(status)
	_, err = l.store.q.AckAgentRunCompletionWithActivity(opCtx, sqlc.AckAgentRunCompletionWithActivityParams{
		RunID: l.Guard.RunID, ExecutorBootID: l.Guard.ExecutorBootID,
		CompletionOutcome: string(outcome), Status: status,
		TurnResult: pgtype.Text{String: string(result), Valid: true},
	})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("ack AgentRun completion: %w", err)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return l.completionStateError(opCtx, outcome)
	}
	if err := l.rememberTerminalLocked(string(outcome), status, row.CompletionReason); err != nil {
		return err
	}
	// Unknown means the durable acknowledgement was recorded as ambiguous;
	// recording that fact succeeded, so an identical retry is idempotent.
	return nil
}

func turnResultForStatus(status string) turnResult {
	switch status {
	case StatusCompleted:
		return turnResultSuccess
	case StatusCanceled, StatusAborted:
		return turnResultCanceled
	default:
		return turnResultError
	}
}

func completionStatus(prepared string, outcome runcontrol.Outcome) string {
	switch outcome {
	case runcontrol.OutcomeFailed:
		return StatusFailed
	case runcontrol.OutcomeUnknown:
		return StatusInterrupted
	case runcontrol.OutcomeDiscarded:
		return StatusCanceled
	}
	return prepared
}

func (l *Lease) completionStateError(ctx context.Context, wantOutcome runcontrol.Outcome) error {
	row, err := l.store.q.GetAgentRun(ctx, l.Guard.RunID)
	if err != nil {
		return fmt.Errorf("ack AgentRun completion: %w", err)
	}
	if row.Status != "running" {
		return l.observeTerminalLocked(row.CompletionOutcome, row.CompletionStatus, row.CompletionReason, wantOutcome, row.CompletionStatus, row.CompletionReason)
	}
	if row.CompletionState == "unknown" || row.CompletionOutcome == string(runcontrol.OutcomeUnknown) {
		return ErrOutcomeUnknown
	}
	return ErrLeaseLost
}

// WaitTerminal waits for a durable terminal transition. It is useful to keep
// the stream owner alive until the adapter has acknowledged its final effect.
func (l *Lease) WaitTerminal(ctx context.Context) error {
	if l == nil {
		return ErrLeaseLost
	}
	select {
	case <-l.terminal:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
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

// Abort terminalizes the current owner after RequestAbort has durably recorded
// the stop request. The reason is read from that durable request, so a canceled
// model turn cannot race the stop path with a different terminal explanation.
// It binds only the Store lifetime to the database call: the runtime may
// already have canceled Lease.Context while unwinding the turn.
func (l *Lease) Abort(ctx context.Context) error {
	if l == nil || l.store == nil {
		return errors.New("AgentRun lease is not configured")
	}
	l.transitionMu.Lock()
	defer l.transitionMu.Unlock()
	if l.finished {
		if l.finishedStatus == StatusAborted {
			return nil
		}
		if l.finishedOutcome == runcontrol.OutcomeUnknown {
			return ErrOutcomeUnknown
		}
		return ErrCompletionConflict
	}
	if l.store.closed.Load() || l.store.lifecycleCtx.Err() != nil {
		return ErrStoreClosed
	}
	opCtx, release := l.store.bindLifecycle(ctx)
	defer release()
	row, err := l.store.q.GetAgentRun(opCtx, l.Guard.RunID)
	if err != nil {
		return fmt.Errorf("read AgentRun abort request: %w", err)
	}
	if row.Status != "running" {
		return l.observeTerminalLocked(row.CompletionOutcome, row.CompletionStatus, row.CompletionReason, runcontrol.OutcomeDiscarded, row.CompletionStatus, row.CompletionReason)
	}
	if !row.AbortRequestedAt.Valid {
		return ErrLeaseLost
	}
	reason := row.AbortReason
	if reason == "" {
		reason = "abort_requested"
	}
	_, err = l.store.q.AbortAgentRunWithActivity(opCtx, sqlc.AbortAgentRunWithActivityParams{
		RunID: l.Guard.RunID, ExecutorBootID: l.Guard.ExecutorBootID, Reason: reason,
	})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("abort AgentRun: %w", err)
	}
	row, getErr := l.store.q.GetAgentRun(opCtx, l.Guard.RunID)
	if getErr != nil {
		if errors.Is(err, pgx.ErrNoRows) && errors.Is(getErr, pgx.ErrNoRows) {
			return ErrLeaseLost
		}
		return fmt.Errorf("read aborted AgentRun: %w", getErr)
	}
	if row.Status == "running" {
		// Either the request was not durable yet or the lease expired before
		// this owner could close it. Reap owns those states.
		return ErrLeaseLost
	}
	if row.Status != StatusAborted {
		return l.observeTerminalLocked(row.CompletionOutcome, row.CompletionStatus, row.CompletionReason, runcontrol.OutcomeDiscarded, row.CompletionStatus, row.CompletionReason)
	}
	if err := l.rememberTerminalLocked(row.CompletionOutcome, row.CompletionStatus, row.CompletionReason); err != nil {
		return err
	}
	if l.finishedOutcome == runcontrol.OutcomeUnknown {
		return ErrOutcomeUnknown
	}
	return nil
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

// reconcileLocalLeases repairs missed terminal notifications from another
// executor. Reap's terminalization queries only return rows this invocation
// changed; a local lease may therefore already be terminal in PostgreSQL while
// its heartbeat worker is merely canceled. Only a confirmed non-running row
// may settle the local completion barrier. Query failures and still-running
// rows remain live so a transient database failure cannot invent a terminal
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
		lease.reconcileTerminal(row)
	}
}

func (l *Lease) reconcileTerminal(row sqlc.AgentRun) {
	l.transitionMu.Lock()
	defer l.transitionMu.Unlock()
	if l.finished || row.Status == "running" {
		return
	}
	// AgentRun's CHECK constraint guarantees a valid outcome. If a corrupt or
	// future row reaches this path, fail closed and leave the lease unsettled;
	// fabricating an unknown terminal result would let a later Ack(Unknown)
	// succeed without a durable unknown outcome.
	if err := l.rememberTerminalLocked(row.CompletionOutcome, row.CompletionStatus, row.CompletionReason); err != nil {
		return
	}
}

func (s *Store) reapLease(id string) {
	s.mu.Lock()
	lease := s.local[id]
	s.mu.Unlock()
	if lease != nil {
		lease.cancel(ErrLeaseLost)
		lease.signalTerminal()
		s.mu.Lock()
		delete(s.local, id)
		s.mu.Unlock()
	}
}
