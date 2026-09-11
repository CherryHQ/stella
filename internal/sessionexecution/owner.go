package sessionexecution

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/internal/core/agenterr"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// Owner identifies the process that claimed a session execution lease. Host
// and PID let a claimant prove the previous writer exited before taking over
// its writable environment (plan D9): a lease that outlives its process is
// claimable; a lease held by a live or unverifiable owner is not.
type Owner struct {
	ID   string // worker id or process role, for logs and admin surfaces
	Host string
	PID  int
}

// ErrWriterAlive means the expired lease's recorded owner is a live process
// on this host — takeover would share one writable environment with a writer
// that may still be mid-turn.
var ErrWriterAlive = errors.New("sessionexecution: previous writer still alive")

// ErrWriterUnverifiable means the expired lease's owner cannot be proven dead
// from this host (different machine, missing owner record, or an uncheckable
// platform). The session stays unavailable until the writer releases the row
// itself or an operator clears it — never a blind lease-expiry takeover.
var ErrWriterUnverifiable = errors.New("sessionexecution: previous writer exit unverifiable")

// ProcessOwner returns this process's owner record. id distinguishes the
// claimant role (worker id, "admission") inside the process.
func ProcessOwner(id string) Owner {
	host, _ := os.Hostname()
	return Owner{ID: id, Host: host, PID: os.Getpid()}
}

// ClaimSessionTx is the shared atomic claim: it serializes on the session's
// execution row, refuses live leases and writers that cannot be proven dead,
// then stamps the new owner in the same transaction.
func ClaimSessionTx(ctx context.Context, tx pgx.Tx, sessionID, token string, owner Owner) error {
	q := sqlc.New(tx)
	prev, err := q.LockSessionExecutionForClaim(ctx, sessionID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// No prior writer: free claim.
	case err != nil:
		return err
	default:
		if prev.LeaseUntil.After(time.Now()) {
			return agenterr.ErrSessionBusy
		}
		// Busy-shaped so callers keep their existing 409/skip handling; the
		// concrete reason stays distinguishable via errors.Is.
		if err := predecessorExited(prev); err != nil {
			return fmt.Errorf("%w: %w", agenterr.ErrSessionBusy, err)
		}
	}
	_, err = q.ClaimSessionExecution(ctx, sqlc.ClaimSessionExecutionParams{
		SessionID: sessionID,
		Token:     token,
		OwnerID:   pgtype.Text{String: owner.ID, Valid: owner.ID != ""},
		OwnerHost: pgtype.Text{String: owner.Host, Valid: true},
		OwnerPid:  pgtype.Int4{Int32: int32(owner.PID), Valid: owner.PID > 0},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// Lost the race between the read and the update.
		return agenterr.ErrSessionBusy
	}
	return err
}

// predecessorExited decides whether an expired lease's recorded writer is
// gone. Same-host PIDs are checkable; anything else is fail-closed.
func predecessorExited(prev sqlc.CtxSessionExecution) error {
	if !prev.OwnerPid.Valid || !prev.OwnerHost.Valid || prev.OwnerHost.String == "" {
		return ErrWriterUnverifiable
	}
	host, _ := os.Hostname()
	if prev.OwnerHost.String != host {
		return fmt.Errorf("%w: owner %q on host %q", ErrWriterUnverifiable, prev.OwnerID.String, prev.OwnerHost.String)
	}
	if pidAlive(int(prev.OwnerPid.Int32)) {
		return fmt.Errorf("%w: owner %q pid %d", ErrWriterAlive, prev.OwnerID.String, prev.OwnerPid.Int32)
	}
	return nil
}

// WriterExited reports whether a recorded owner can be proven dead — the
// reaper uses it to decide whether an expired row may be deleted.
func WriterExited(ownerHost pgtype.Text, ownerPid pgtype.Int4) bool {
	if !ownerHost.Valid || !ownerPid.Valid || ownerHost.String == "" {
		return false
	}
	host, _ := os.Hostname()
	if ownerHost.String != host {
		return false
	}
	return !pidAlive(int(ownerPid.Int32))
}

// releaseExpiredRetry retries the fenced-out writer's exit attest: after a
// failed Finish the row may sit expired with a still-alive owner, which would
// deny every future takeover. Deletes match only our token while expired, so
// a re-claimed or re-leased row is never touched; a clean delete or a missing
// row ends the loop.
func (s *Store) releaseExpiredRetry(sessionID, token string) {
	// ~60s covers a full lease TTL plus outage jitter: the row may not be
	// expired yet when the retry starts.
	for range 40 {
		time.Sleep(1500 * time.Millisecond)
		ctx, cancel := context.WithTimeout(context.Background(), OperationTimeout)
		row, err := sqlc.New(s.db).GetSessionExecution(ctx, sessionID)
		cancel()
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && row.Token != token) {
			return // attested already, or re-claimed by a successor
		}
		if err != nil {
			slog.Debug("release expired execution lease probe failed", "session", sessionID, "error", err)
			continue
		}
		ctx, cancel = context.WithTimeout(context.Background(), OperationTimeout)
		if _, err := sqlc.New(s.db).DeleteExpiredSessionExecution(ctx, sqlc.DeleteExpiredSessionExecutionParams{
			SessionID: sessionID,
			Token:     token,
		}); err != nil {
			slog.Debug("release expired execution lease failed", "session", sessionID, "error", err)
		}
		cancel()
	}
}
