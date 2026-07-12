package channel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// IngressLease is a single-leader election over ALL channel ingress. Every
// replica runs one, but only the current lease holder starts the managed channel
// bot pollers; the rest stand by. This makes multi-replica deployments safe:
// without it, every replica polls every platform, producing Telegram 409
// conflicts and duplicate delivery on weixin/qq/feishu.
//
// The lease is a single Postgres row (channel_ingress_lease). Acquire/renew is an
// atomic CAS that stamps lease_expires_at from the DATABASE clock, so failover
// timing is decided by one clock and never depends on a replica's local time. A
// holder renews every ttl/3; a standby that observes the lease free or expired
// takes it over.
//
// Fail-safe over split-brain: if renews stop succeeding (DB error, or another
// replica has taken the lease) the holder stops leading BEFORE its lease could
// expire, using a purely-local conservative deadline. Losing the DB means no
// polling — never two concurrent pollers. The lease is always-on: a single
// replica wins on its first attempt with near-zero overhead.
type IngressLease struct {
	q       *sqlc.Queries
	ownerID string
	ttl     time.Duration
	log     *slog.Logger
	now     func() time.Time
}

// NewIngressLease builds a lease bound to pool, identified by ownerID (a stable
// per-process id — see DefaultOwnerID), renewing within ttl.
func NewIngressLease(pool *pgxpool.Pool, ownerID string, ttl time.Duration, log *slog.Logger) *IngressLease {
	if log == nil {
		log = slog.Default()
	}
	return &IngressLease{
		q:       sqlc.New(pool),
		ownerID: ownerID,
		ttl:     ttl,
		log:     log.With("component", "channel_ingress_lease", "owner_id", ownerID),
		now:     time.Now,
	}
}

// DefaultOwnerID returns a stable per-process identity for a lease holder:
// hostname + pid. Two processes never collide, and a restarted process gets a
// fresh id (so it cannot renew a lease its dead predecessor held).
func DefaultOwnerID() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown-host"
	}
	return fmt.Sprintf("%s/%d", host, os.Getpid())
}

// Run drives the lease until ctx is cancelled. On first acquisition it creates a
// child leaderCtx and calls onAcquire(leaderCtx); while held it renews every
// ttl/3. If leadership is lost (or can no longer be guaranteed) it cancels
// leaderCtx and calls onRelease, and it may reacquire later — onAcquire/onRelease
// must therefore be repeatable across flapping failover. On ctx cancel it cancels
// leaderCtx, calls onRelease, best-effort releases the DB lease so a peer can take
// over immediately, and returns ctx.Err().
func (l *IngressLease) Run(ctx context.Context, onAcquire func(leaderCtx context.Context), onRelease func()) error {
	interval := l.ttl / 3
	if interval <= 0 {
		interval = l.ttl
	}

	var (
		held         bool
		leaderCancel context.CancelFunc
		// heldUntil is a conservative, purely-local deadline: last successful
		// acquire/renew observed locally + ttl. Because our request reaches the DB
		// no earlier than we send it, heldUntil is always <= the DB-stamped
		// lease_expires_at, so relinquishing at heldUntil never overlaps a peer that
		// reacquires at the DB deadline.
		heldUntil time.Time
	)

	stopLeading := func() {
		if !held {
			return
		}
		held = false
		if leaderCancel != nil {
			leaderCancel()
			leaderCancel = nil
		}
		onRelease()
	}

	// When a renew errors (uncertain outcome), keep leading only while we are safely
	// inside the local deadline (more than one renew interval of margin). This
	// tolerates a single transient blip but relinquishes one interval before the
	// lease could expire, so a peer never starts polling while we still are.
	safeToKeepLeading := func(now time.Time) bool {
		return now.Before(heldUntil.Add(-interval))
	}

	attempt := func() {
		now := l.now()
		// The DB-stamped lease_expires_at is discarded on purpose: heldUntil is
		// derived from the LOCAL clock (now + ttl) so the drop decision below never
		// mixes clocks. Because our request reaches the DB no earlier than now,
		// heldUntil <= the DB deadline, keeping relinquish strictly ahead of a peer.
		_, err := l.q.AcquireChannelIngressLease(ctx, sqlc.AcquireChannelIngressLeaseParams{
			OwnerID:    l.ownerID,
			TtlSeconds: l.ttl.Seconds(),
		})
		switch {
		case err == nil:
			// Acquired or renewed.
			heldUntil = now.Add(l.ttl)
			if !held {
				var leaderCtx context.Context
				leaderCtx, leaderCancel = context.WithCancel(ctx)
				held = true
				l.log.Info("acquired channel ingress leadership")
				onAcquire(leaderCtx)
			}
		case errors.Is(err, pgx.ErrNoRows):
			// The CAS only fails with no rows when another owner holds a LIVE
			// (non-expired) lease — meaning our lease already expired DB-side and was
			// reassigned. Ownership is definitively lost, so stop immediately rather
			// than riding out the local safe window (which, under clock skew, could
			// keep us polling alongside the new owner — the exact split-brain to avoid).
			if held {
				l.log.Warn("lost channel ingress leadership to another replica; stopping pollers")
				stopLeading()
			}
		default:
			// DB error: we cannot confirm whether we still hold the lease. Keep leading
			// only while safely inside the local deadline (tolerates a transient blip),
			// and relinquish before the lease could expire.
			if held && !safeToKeepLeading(now) {
				l.log.Warn("cannot renew channel ingress lease before expiry; stopping pollers (fail-safe)", "error", err)
				stopLeading()
			} else if !held {
				l.log.Debug("channel ingress lease acquire failed", "error", err)
			}
		}
	}

	// Elect promptly, then renew on a fixed cadence.
	attempt()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			stopLeading()
			l.release()
			return ctx.Err()
		case <-ticker.C:
			attempt()
		}
	}
}

// release frees the DB lease on graceful shutdown so a peer acquires immediately
// instead of waiting for expiry. Best-effort: a detached context is used because
// the caller's ctx is already cancelled, and any error is only logged.
func (l *IngressLease) release() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := l.q.ReleaseChannelIngressLease(ctx, l.ownerID); err != nil {
		l.log.Debug("release channel ingress lease failed", "error", err)
	}
}
