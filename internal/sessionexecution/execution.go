// Package sessionexecution owns the current database execution right of a Session.
// A token fences writes, not external requests or goroutines already in flight.
package sessionexecution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

const (
	OperationTimeout = 5 * time.Second
	renewInterval    = 5 * time.Second
	reapInterval     = 10 * time.Second
)

var (
	ErrLost           = errors.New("session execution is no longer current")
	ErrOutcomeUnknown = errors.New("session execution completion outcome unknown")
)

type Store struct {
	db     *pgxpool.Pool
	commit func(context.Context, pgx.Tx) error
}

func New(db *pgxpool.Pool) *Store {
	return &Store{db: db, commit: func(ctx context.Context, tx pgx.Tx) error { return tx.Commit(ctx) }}
}

type Lease struct {
	store     *Store
	sessionID string
	token     string
	extra     FinishExtra
	ctx       context.Context
	cancel    context.CancelCauseFunc
	stop      chan struct{}
	done      chan struct{}
	once      sync.Once
}

type (
	executionKey struct{}
	requiredKey  struct{}
)

// Require marks the execution boundary before any slow preparation. Dropping a
// token while retaining this context fails closed, including WithoutCancel.
func Require(ctx context.Context) context.Context { return context.WithValue(ctx, requiredKey{}, true) }

func FromContext(ctx context.Context) *Lease {
	lease, _ := ctx.Value(executionKey{}).(*Lease)
	return lease
}

// SessionID is the session this lease fences. An adoption check must compare
// it to the admission target — a lease inherited from a parent turn's context
// belongs to the parent session and must never substitute for the child's own
// claim.
func (l *Lease) SessionID() string { return l.sessionID }

// Token is the execution identity stamped onto this turn's durable events.
// Observers tail ctx_session_event by it and stop only at its terminal
// marker — never at this row disappearing.
func (l *Lease) Token() string { return l.token }

// FinishExtra runs inside the lease-finish transaction after the token
// validates and before commit. It is where a run worker folds its own durable
// completion (run state, reply ops) into the same atomic unit as the lease's
// activity finish and execution-row delete.
type FinishExtra func(ctx context.Context, tx pgx.Tx, result string) error

// ClaimWith is Claim plus a finish-transaction callback.
func (s *Store) ClaimWith(ctx context.Context, sessionID string, extra FinishExtra) (context.Context, *Lease, error) {
	return s.claim(ctx, sessionID, extra)
}

func (s *Store) Claim(ctx context.Context, sessionID string) (context.Context, *Lease, error) {
	return s.claim(ctx, sessionID, nil)
}

// Adopt wraps a token the caller already claimed inside its own transaction —
// the run worker claims run and session execution in one commit, then adopts
// the lease here so renewal, cancellation and Finish behave exactly like a
// locally claimed lease.
func (s *Store) Adopt(ctx context.Context, sessionID, token string, extra FinishExtra) (context.Context, *Lease) {
	runCtx, cancelRun := context.WithCancelCause(Require(ctx))
	lease := &Lease{store: s, sessionID: sessionID, token: token, extra: extra, ctx: runCtx, cancel: cancelRun, stop: make(chan struct{}), done: make(chan struct{})}
	runCtx = context.WithValue(runCtx, executionKey{}, lease)
	go lease.maintain()
	return runCtx, lease
}

func (s *Store) claim(ctx context.Context, sessionID string, extra FinishExtra) (context.Context, *Lease, error) {
	opCtx, cancel := context.WithTimeout(ctx, OperationTimeout)
	defer cancel()
	tx, err := s.db.Begin(opCtx)
	if err != nil {
		return nil, nil, err
	}
	defer rollback(tx)
	q := sqlc.New(tx)
	token := uuid.Must(uuid.NewV7()).String()
	// The admission claimant's writer identity is this process: a takeover
	// must first prove the previous writer exited (plan D9).
	if err := ClaimSessionTx(opCtx, tx, sessionID, token, ProcessOwner("admission")); err != nil {
		return nil, nil, err
	}
	rows, err := q.StartSessionExecutionActivity(opCtx, sessionID)
	if err != nil {
		return nil, nil, err
	}
	if rows != 1 {
		return nil, nil, fmt.Errorf("session %s is unavailable", sessionID)
	}
	if err := tx.Commit(opCtx); err != nil {
		return nil, nil, fmt.Errorf("claim session execution (outcome unknown): %w", err)
	}
	runCtx, cancelRun := context.WithCancelCause(Require(ctx))
	lease := &Lease{store: s, sessionID: sessionID, token: token, extra: extra, ctx: runCtx, cancel: cancelRun, stop: make(chan struct{}), done: make(chan struct{})}
	runCtx = context.WithValue(runCtx, executionKey{}, lease)
	go lease.maintain()
	return runCtx, lease, nil
}

func (l *Lease) maintain() {
	defer close(l.done)
	ticker := time.NewTicker(renewInterval)
	defer ticker.Stop()
	for {
		select {
		case <-l.stop:
			return
		case <-l.ctx.Done():
			return
		case <-ticker.C:
			if err := l.Renew(l.ctx); err != nil {
				l.cancel(fmt.Errorf("%w: %w", ErrLost, err))
				return
			}
		}
	}
}

func (l *Lease) Renew(ctx context.Context) error {
	if l.ctx.Err() != nil {
		return ErrLost
	}
	ctx, cancel := context.WithTimeout(ctx, OperationTimeout)
	defer cancel()
	tx, err := l.store.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	q := sqlc.New(tx)
	if _, err := q.LockSessionExecutionForFinish(ctx, sqlc.LockSessionExecutionForFinishParams{SessionID: l.sessionID, Token: l.token}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrLost
		}
		return err
	}
	n, err := q.RenewSessionExecution(ctx, sqlc.RenewSessionExecutionParams{SessionID: l.sessionID, Token: l.token})
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrLost
	}
	return tx.Commit(ctx)
}

// Check prevents new model/tool work after a locally observed loss. Database
// mutations additionally call ValidateTx, since a local check cannot fence SQL.
func Check(ctx context.Context) error {
	l := FromContext(ctx)
	if l == nil {
		if required, _ := ctx.Value(requiredKey{}).(bool); required {
			return ErrLost
		}
		return ctx.Err()
	}
	if l.ctx.Err() != nil {
		return fmt.Errorf("%w: %w", ErrLost, context.Cause(l.ctx))
	}
	return ctx.Err()
}

// ValidateTx takes the execution row before business row locks. The shared lock
// prevents takeover until this short transaction has committed or rolled back.
func ValidateTx(ctx context.Context, tx pgx.Tx) error {
	if err := Check(ctx); err != nil {
		return err
	}
	l := FromContext(ctx)
	if l == nil {
		return nil
	} // Explicitly unmarked user APIs and accepted background jobs.
	opCtx, cancel := context.WithTimeout(ctx, OperationTimeout)
	defer cancel()
	if _, err := tx.Exec(opCtx, "SET LOCAL statement_timeout = '5s'; SET LOCAL idle_in_transaction_session_timeout = '5s'; SET LOCAL transaction_timeout = '5s'"); err != nil {
		return l.fail(err)
	}
	_, err := sqlc.New(tx).ValidateSessionExecution(opCtx, sqlc.ValidateSessionExecutionParams{SessionID: l.sessionID, Token: l.token})
	if err != nil {
		return l.fail(err)
	}
	valid, err := sqlc.New(tx).SessionExecutionValid(opCtx, sqlc.SessionExecutionValidParams{SessionID: l.sessionID, Token: l.token})
	if err != nil {
		return l.fail(err)
	}
	if !valid.Valid || !valid.Bool {
		return l.fail(ErrLost)
	}
	return nil
}

// Abort lets a void observer report a persistence failure through the run context.
func Abort(ctx context.Context, err error) {
	if l := FromContext(ctx); l != nil {
		_ = l.fail(err)
	}
}

func (l *Lease) fail(err error) error {
	lost := fmt.Errorf("%w: %w", ErrLost, err)
	l.cancel(lost)
	return lost
}

// Finish is the only canceled-execution write: activity and token deletion are
// atomic. A lost commit response never retries the run or its result commits.
func (l *Lease) Finish(result string) error {
	if err := l.finish(result); err != nil {
		// The turn is over but the attest may not have landed (DB outage,
		// lost commit). Retry briefly in the background so an unwound writer
		// does not leave a tombstone that denies takeover forever.
		go l.store.releaseExpiredRetry(l.sessionID, l.token)
		return err
	}
	return nil
}

func (l *Lease) finish(result string) error {
	l.once.Do(func() { close(l.stop) })
	<-l.done
	ctx, cancel := context.WithTimeout(context.WithoutCancel(l.ctx), OperationTimeout)
	defer cancel()
	tx, err := l.store.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	q := sqlc.New(tx)
	row, err := q.LockSessionExecutionForFinish(ctx, sqlc.LockSessionExecutionForFinishParams{SessionID: l.sessionID, Token: l.token})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrLost
	}
	if err != nil {
		return err
	}
	if err := lockSessionForTerminal(ctx, q, l.sessionID); err != nil {
		return err
	}
	valid, err := q.SessionExecutionValid(ctx, sqlc.SessionExecutionValidParams{SessionID: l.sessionID, Token: l.token, AllowCancel: true})
	if err != nil {
		return err
	}
	if !valid.Valid || !valid.Bool {
		// The turn already unwound (Finish runs after the executor returned),
		// so this process is no longer a writer. Drop our own expired row so
		// the next claimant sees a confirmed exit instead of an unverifiable
		// tombstone — a re-claimed row has a different token and is never
		// matched. The linked run stays for the reaper: it owns the
		// run→exec lock order and also repairs orphans after this row is gone.
		// The terminal marker still lands for our token: watchers must never
		// infer an end from the row disappearing. Marker and release stay
		// atomic — a failed marker rolls the release back for the retry loop.
		if err := recordTerminalLocked(ctx, q, l.sessionID, l.token, row.RunID.String, "error", "execution lease expired"); err != nil {
			return err
		}
		if _, err := q.DeleteExpiredSessionExecution(ctx, sqlc.DeleteExpiredSessionExecutionParams{SessionID: l.sessionID, Token: l.token}); err == nil {
			_ = l.store.commit(ctx, tx)
		}
		return ErrLost
	}
	switch {
	case row.CancelRequested:
		result = "canceled"
	case errors.Is(context.Cause(l.ctx), ErrLost):
		result = "error"
	case l.ctx.Err() != nil:
		result = "canceled"
	}
	if err := q.FinishSessionExecutionActivity(ctx, sqlc.FinishSessionExecutionActivityParams{SessionID: l.sessionID, Result: pgtype.Text{String: result, Valid: true}}); err != nil {
		return err
	}
	if l.extra != nil {
		if err := l.extra(ctx, tx, result); err != nil {
			return err
		}
	}
	if err := recordTerminalLocked(ctx, q, l.sessionID, l.token, row.RunID.String, result, ""); err != nil {
		return fmt.Errorf("record execution terminal: %w", err)
	}
	if _, err := q.DeleteSessionExecution(ctx, sqlc.DeleteSessionExecutionParams{SessionID: l.sessionID, Token: l.token}); err != nil {
		return err
	}
	if err := l.store.commit(ctx, tx); err != nil {
		return fmt.Errorf("%w: %w", ErrOutcomeUnknown, err)
	}
	l.cancel(context.Canceled)
	return nil
}

// CancelCurrent selects once, then cancels that exact token. A concurrent
// successor is never the target of a delayed stop request.
func (s *Store) CancelCurrent(ctx context.Context, sessionID string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, OperationTimeout)
	defer cancel()
	row, err := sqlc.New(s.db).GetSessionExecution(ctx, sessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer rollback(tx)
	q := sqlc.New(tx)
	if _, err := q.LockSessionExecutionForFinish(ctx, sqlc.LockSessionExecutionForFinishParams{SessionID: sessionID, Token: row.Token}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	n, err := q.CancelSessionExecution(ctx, sqlc.CancelSessionExecutionParams{SessionID: sessionID, Token: row.Token})
	if err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return n == 1, nil
}

func (s *Store) Reap(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, OperationTimeout)
	defer cancel()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	q := sqlc.New(tx)
	rows, err := q.ListExpiredSessionExecutions(ctx)
	if err != nil {
		return err
	}
	for _, row := range rows {
		result := "error"
		if row.CancelRequested {
			result = "canceled"
		}
		if err := lockSessionForTerminal(ctx, q, row.SessionID); err != nil {
			return err
		}
		// The token's terminal is a fact about the turn, not about whether the
		// tombstone may be deleted: a still-alive writer's row stays fenced,
		// but its observers must not wait on a lease that can never write
		// again. The marker is idempotent, so the writer's own finish cannot
		// produce a second verdict.
		if err := recordTerminalLocked(ctx, q, row.SessionID, row.Token, row.RunID.String, result, "execution reaped"); err != nil {
			return err
		}
		if err := q.FinishSessionExecutionActivity(ctx, sqlc.FinishSessionExecutionActivityParams{SessionID: row.SessionID, Result: pgtype.Text{String: result, Valid: true}}); err != nil {
			return err
		}
		// A linked durable run shares this execution's fate: terminating it in
		// the same transaction keeps run state from orphaning as 'running'
		// after the execution row is gone.
		if row.RunID.Valid {
			if _, err := q.MarkAgentRunInterrupted(ctx, row.RunID.String); err != nil {
				return err
			}
		}
		// The row is the previous writer's tombstone: delete it only when the
		// owner is confirmed dead. A live or unverifiable owner keeps the row
		// so takeover claims stay fenced; the fenced-out writer clears it
		// itself via Finish once its turn unwinds (plan D9). The marker above
		// already ended the token's observation regardless of the tombstone.
		if !WriterExited(row.OwnerHost, row.OwnerPid) {
			slog.WarnContext(ctx, "execution lease kept: previous writer not proven dead",
				"session", row.SessionID, "owner", row.OwnerID.String)
			continue
		}
		if _, err := q.DeleteSessionExecution(ctx, sqlc.DeleteSessionExecutionParams{SessionID: row.SessionID, Token: row.Token}); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) RunReaper(ctx context.Context) {
	ticker := time.NewTicker(reapInterval)
	defer ticker.Stop()
	for {
		if err := s.Reap(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("reap session executions", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Lock order for every transaction that writes an execution terminal:
// execution row lock → ctxconv advisory → ctx_conversation row write.
// EventSink.AppendBatch already holds the advisory while it bumps the
// conversation's event_seq row — a caller holding the conversation row before
// taking the advisory would deadlock the two against each other.
func lockSessionForTerminal(ctx context.Context, q *sqlc.Queries, sessionID string) error {
	// The event domain's advisory is keyed by session id — the same lock
	// EventSink.AppendBatch holds while bumping the session's event_seq.
	return q.LockConversationForWrite(ctx, sessionID)
}

// recordTerminalLocked appends the execution's turn_terminal marker inside the
// caller's transaction, which must already hold the conversation write lock.
// The marker is the only durable end-of-turn signal for observers tailing
// ctx_session_event by token: the lease row is deleted on the same commit, so
// nothing may infer an end from the row going away. It is idempotent per
// token — a token retired by reap then self-released keeps its first verdict.
func recordTerminalLocked(ctx context.Context, q *sqlc.Queries, sessionID, token, runID, result, reason string) error {
	if sessionID == "" || token == "" {
		return nil
	}
	if _, err := q.GetSessionTerminalEvent(ctx, sqlc.GetSessionTerminalEventParams{
		SessionID:   sessionID,
		ExecutionID: pgtype.Text{String: token, Valid: true},
	}); err == nil {
		return nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	seq, err := q.NextSessionEventSeq(ctx, sessionID)
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]string{"type": "turn_terminal", "result": result, "reason": reason})
	return q.InsertSessionEvent(ctx, sqlc.InsertSessionEventParams{
		SessionID:   sessionID,
		RunID:       pgtype.Text{String: runID, Valid: runID != ""},
		ExecutionID: pgtype.Text{String: token, Valid: true},
		Seq:         seq,
		Event:       payload,
	})
}
