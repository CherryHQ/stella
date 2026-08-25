// Package agentrun owns the PostgreSQL execution lease shared by every Agent
// entry path. A process-local runtime gate may reject faster, but this package
// is the cross-replica authority.
package agentrun

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

var (
	ErrBusy      = errors.New("agent run already active")
	ErrLeaseLost = errors.New("agent run ownership lost")
)

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

type (
	guardKey       struct{}
	guardedContext struct {
		Guard
		store *Store
	}
)

func WithGuard(ctx context.Context, guard Guard) context.Context {
	return context.WithValue(ctx, guardKey{}, guardedContext{Guard: guard})
}

func GuardFromContext(ctx context.Context) (Guard, bool) {
	value, ok := ctx.Value(guardKey{}).(guardedContext)
	guard := value.Guard
	return guard, ok && guard.RunID != "" && guard.ExecutorBootID != ""
}

// InheritGuard copies the complete ownership fence (including its live Store
// operation checker) from source onto a cancellation/deadline context owned by
// an entry adapter.
func InheritGuard(ctx, source context.Context) (context.Context, bool) {
	value, ok := source.Value(guardKey{}).(guardedContext)
	if !ok || value.RunID == "" || value.ExecutorBootID == "" {
		return ctx, false
	}
	return context.WithValue(ctx, guardKey{}, value), true
}

func withLeaseGuard(ctx context.Context, guard Guard, store *Store) context.Context {
	return context.WithValue(ctx, guardKey{}, guardedContext{Guard: guard, store: store})
}

// Check is the fail-closed model/tool/Sandbox operation fence. Durable writes
// use ValidateTx instead because the check must be coupled to their commit.
func Check(ctx context.Context) error {
	value, ok := ctx.Value(guardKey{}).(guardedContext)
	if !ok || value.store == nil {
		return nil
	}
	_, err := value.store.q.LockAgentRunOwnership(ctx, sqlc.LockAgentRunOwnershipParams{
		RunID: value.RunID, ExecutorBootID: value.ExecutorBootID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrLeaseLost
	}
	if err != nil {
		return fmt.Errorf("check AgentRun ownership: %w", err)
	}
	return nil
}

// ValidateTx takes a row lock compatible with readers but conflicting with a
// terminal UPDATE. The ownership check and caller's write therefore commit in
// one serialization order.
func ValidateTx(ctx context.Context, tx pgx.Tx) error {
	guard, ok := GuardFromContext(ctx)
	if !ok {
		return nil
	}
	_, err := sqlc.New(tx).LockAgentRunOwnership(ctx, sqlc.LockAgentRunOwnershipParams{
		RunID: guard.RunID, ExecutorBootID: guard.ExecutorBootID,
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
	db             *pgxpool.Pool
	q              *sqlc.Queries
	executorBootID string
	lease          time.Duration
	sandboxCleaner func(context.Context, string, string) error
	mu             sync.Mutex
	local          map[string]context.CancelCauseFunc
}

func NewStore(db *pgxpool.Pool, executorBootID string, sandboxCleaner ...func(context.Context, string, string) error) *Store {
	var cleaner func(context.Context, string, string) error
	if len(sandboxCleaner) > 0 {
		cleaner = sandboxCleaner[0]
	}
	return &Store{
		db: db, q: sqlc.New(db), executorBootID: executorBootID,
		lease: defaultLease, sandboxCleaner: cleaner,
		local: make(map[string]context.CancelCauseFunc),
	}
}

func NewBootID() string { return uuid.Must(uuid.NewV7()).String() }

func (s *Store) ExecutorBootID() string {
	if s == nil {
		return ""
	}
	return s.executorBootID
}

type Lease struct {
	Guard  Guard
	ctx    context.Context
	cancel context.CancelCauseFunc
	store  *Store
	done   chan struct{}
	once   sync.Once
}

func (l *Lease) Context() context.Context { return withLeaseGuard(l.ctx, l.Guard, l.store) }

func (s *Store) Acquire(ctx context.Context, sessionID, source string) (*Lease, error) {
	return s.acquire(ctx, sessionID, source, "", "", "")
}

// AcquireForInbox atomically links a pending Session inbox receipt to the new
// Run. A crash can therefore expose either an unlinked receipt with no Run or a
// linked receipt with its Run, never half of the admission decision.
func (s *Store) AcquireForInbox(ctx context.Context, sessionID, source, inboxID string) (*Lease, error) {
	if inboxID == "" {
		return nil, errors.New("session inbox ID is required")
	}
	return s.acquire(ctx, sessionID, source, inboxID, "", "")
}

// AcquireForChannelFIFO atomically links the claimed durable channel input to
// the Run created for it. The claim token prevents an expired claimant from
// linking a replacement Run.
func (s *Store) AcquireForChannelFIFO(ctx context.Context, sessionID, source, fifoID, claimToken string) (*Lease, error) {
	if fifoID == "" || claimToken == "" {
		return nil, errors.New("channel FIFO ID and claim token are required")
	}
	return s.acquire(ctx, sessionID, source, "", fifoID, claimToken)
}

func (s *Store) acquire(ctx context.Context, sessionID, source, inboxID, fifoID, claimToken string) (*Lease, error) {
	if s == nil || s.db == nil || s.q == nil || s.executorBootID == "" {
		return nil, errors.New("AgentRun store is not configured")
	}
	if sessionID == "" || source == "" {
		return nil, errors.New("AgentRun session and source are required")
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin AgentRun admission: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	if _, err := qtx.InterruptExpiredAgentRunBySession(ctx, sessionID); err != nil {
		return nil, fmt.Errorf("terminalize expired AgentRun: %w", err)
	}
	id := uuid.Must(uuid.NewV7()).String()
	row, err := qtx.CreateAgentRun(ctx, sqlc.CreateAgentRunParams{
		ID: id, SessionID: sessionID, ExecutorBootID: s.executorBootID,
		Source: source, LeaseSeconds: int32(s.lease / time.Second),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrBusy
	}
	if err != nil {
		return nil, fmt.Errorf("create AgentRun: %w", err)
	}
	if inboxID != "" {
		rows, err := qtx.LinkSessionInboxRun(ctx, sqlc.LinkSessionInboxRunParams{
			RunID: pgtype.Text{String: row.ID, Valid: true}, ID: inboxID, TargetSessionID: row.SessionID,
		})
		if err != nil {
			return nil, fmt.Errorf("link Session inbox AgentRun: %w", err)
		}
		if rows != 1 {
			return nil, errors.New("session inbox is no longer pending or already linked")
		}
	}
	if fifoID != "" {
		rows, err := qtx.LinkChannelBindingFIFORun(ctx, sqlc.LinkChannelBindingFIFORunParams{
			RunID: pgtype.Text{String: row.ID, Valid: true}, ID: fifoID,
			ClaimToken: pgtype.Text{String: claimToken, Valid: true},
		})
		if err != nil {
			return nil, fmt.Errorf("link channel FIFO AgentRun: %w", err)
		}
		if rows != 1 {
			return nil, errors.New("channel FIFO claim is no longer owned or already linked")
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit AgentRun admission: %w", err)
	}
	runCtx, cancel := context.WithCancelCause(ctx)
	lease := &Lease{
		Guard: Guard{RunID: row.ID, SessionID: row.SessionID, ExecutorBootID: row.ExecutorBootID},
		ctx:   runCtx, cancel: cancel, store: s, done: make(chan struct{}),
	}
	s.mu.Lock()
	s.local[row.ID] = cancel
	s.mu.Unlock()
	go lease.heartbeat()
	return lease, nil
}

func (l *Lease) heartbeat() {
	interval := l.store.lease / 3
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	defer close(l.done)
	for {
		select {
		case <-l.ctx.Done():
			return
		case <-ticker.C:
			_, err := l.store.q.HeartbeatAgentRun(l.ctx, sqlc.HeartbeatAgentRunParams{
				RunID: l.Guard.RunID, ExecutorBootID: l.Guard.ExecutorBootID,
				LeaseSeconds: int32(l.store.lease / time.Second),
			})
			if err != nil {
				l.cancel(ErrLeaseLost)
				return
			}
		}
	}
}

// Finish is the only owner terminal transition. Abort intent wins once its
// transaction commits; a completion CAS then affects zero rows.
func (l *Lease) Finish(ctx context.Context, status, reason string) error {
	var finishErr error
	l.once.Do(func() {
		l.cancel(nil)
		<-l.done
		l.store.mu.Lock()
		delete(l.store.local, l.Guard.RunID)
		l.store.mu.Unlock()
		if errors.Is(context.Cause(l.ctx), ErrLeaseLost) {
			finishErr = ErrLeaseLost
			return
		}
		rows, err := l.store.q.CompleteAgentRun(ctx, sqlc.CompleteAgentRunParams{
			RunID: l.Guard.RunID, ExecutorBootID: l.Guard.ExecutorBootID,
			Status: status, Reason: reason,
		})
		if err != nil {
			finishErr = fmt.Errorf("complete AgentRun: %w", err)
		} else if rows == 0 {
			// Abort intent or expiry won. Converge an owner-observed abort now;
			// otherwise a reaper makes expiry durably interrupted.
			if abortRows, abortErr := l.store.q.AbortAgentRun(ctx, sqlc.AbortAgentRunParams{
				RunID: l.Guard.RunID, ExecutorBootID: l.Guard.ExecutorBootID, Reason: "abort_requested",
			}); abortErr != nil {
				finishErr = fmt.Errorf("terminalize aborted AgentRun: %w", abortErr)
			} else if abortRows == 0 {
				finishErr = ErrLeaseLost
			}
		}
	})
	return finishErr
}

func (s *Store) RequestAbort(ctx context.Context, sessionID, reason string) (string, error) {
	row, err := s.q.RequestSessionAgentRunAbort(ctx, sqlc.RequestSessionAgentRunAbortParams{SessionID: sessionID, Reason: reason})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("request AgentRun abort: %w", err)
	}
	s.mu.Lock()
	cancel := s.local[row.ID]
	s.mu.Unlock()
	if cancel != nil {
		cancel(context.Canceled)
	}
	return row.ID, nil
}

func (s *Store) LinkInbox(ctx context.Context, inboxID string, guard Guard) error {
	rows, err := s.q.LinkSessionInboxRun(ctx, sqlc.LinkSessionInboxRunParams{
		RunID: pgtype.Text{String: guard.RunID, Valid: true}, ID: inboxID, TargetSessionID: guard.SessionID,
	})
	if err != nil {
		return fmt.Errorf("link Session inbox AgentRun: %w", err)
	}
	if rows != 1 {
		return errors.New("session inbox is no longer pending or already linked")
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

func (s *Store) Reap(ctx context.Context) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin AgentRun reap: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	if _, err := qtx.ReapAbortRequestedAgentRun(ctx, 100); err != nil {
		return fmt.Errorf("reap aborted AgentRun: %w", err)
	}
	if _, err := qtx.ReapExpiredAgentRun(ctx, 100); err != nil {
		return fmt.Errorf("reap expired AgentRun: %w", err)
	}
	if _, err := qtx.TerminalizeLinkedSessionInbox(ctx); err != nil {
		return fmt.Errorf("terminalize linked Session inbox: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit AgentRun reap: %w", err)
	}
	return s.reapSandboxes(ctx)
}

func (s *Store) reapSandboxes(ctx context.Context) error {
	staleSeconds := int32(s.lease / time.Second)
	if _, err := s.q.FenceRecoverableSessionSandbox(ctx, staleSeconds); err != nil {
		return fmt.Errorf("fence orphaned SessionSandbox: %w", err)
	}
	rows, err := s.q.ListRecoverableFencedSessionSandbox(ctx, staleSeconds)
	if err != nil {
		return fmt.Errorf("list orphaned SessionSandbox: %w", err)
	}
	var cleanupErr error
	for _, row := range rows {
		if s.sandboxCleaner == nil {
			cleanupErr = errors.Join(cleanupErr, errors.New("SessionSandbox cleanup is not configured"))
			continue
		}
		cleanupCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		cleanupCtx, err = s.withSandboxProcessIdentities(cleanupCtx, row.SessionID, row.Generation)
		if err != nil {
			cancel()
			cleanupErr = errors.Join(cleanupErr, err)
			continue
		}
		err := s.sandboxCleaner(cleanupCtx, row.ResourceBackend, row.ResourceID)
		cancel()
		if err != nil {
			// Leave the generation fenced. A later pass retries cleanup, and no
			// replacement can start until one pass proves resource absence.
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("clean SessionSandbox %s generation %d: %w", row.SessionID, row.Generation, err))
			continue
		}
		updated, err := s.q.DestroySessionSandboxGeneration(ctx, sqlc.DestroySessionSandboxGenerationParams{
			SessionID: row.SessionID, Generation: row.Generation,
		})
		if err != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("mark SessionSandbox %s generation %d destroyed: %w", row.SessionID, row.Generation, err))
			continue
		}
		if updated == 0 {
			current, getErr := s.q.GetSessionSandbox(ctx, row.SessionID)
			if getErr != nil || current.Generation != row.Generation || current.State != "destroyed" {
				cleanupErr = errors.Join(cleanupErr, ErrLeaseLost, getErr)
			}
		}
	}
	return cleanupErr
}

func (s *Store) withSandboxProcessIdentities(ctx context.Context, sessionID string, generation int64) (context.Context, error) {
	rows, err := s.q.ListSessionSandboxProcess(ctx, sqlc.ListSessionSandboxProcessParams{
		SessionID: sessionID, Generation: generation,
	})
	if err != nil {
		return ctx, err
	}
	identities := make([]pkgsandbox.ProcessIdentity, 0, len(rows))
	for _, row := range rows {
		identities = append(identities, pkgsandbox.ProcessIdentity{PID: int(row.Pid), StartTime: uint64(row.StartTime)})
	}
	return pkgsandbox.WithProcessIdentities(ctx, identities), nil
}

func (s *Store) RunReaper(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		_ = s.Reap(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
