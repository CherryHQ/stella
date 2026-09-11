package host

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// Channel leases: which replica currently runs one channel instance. The row
// is the only authority — a replica may poll, checkpoint, or send only while
// it holds a fresh token pair.

const (
	channelLeaseRenewInterval = 10 * time.Second
	channelLeaseSweepInterval = 15 * time.Second
)

// ChannelLeases tracks the runtime leases this replica holds. Reconcile asks
// Ensure before starting a channel; the tracker renews held leases, releases
// them on disable/delete, and triggers a re-reconcile when a lease is lost so
// the managed runtime stops promptly.
type ChannelLeases struct {
	q       *sqlc.Queries
	ownerID string
	log     *slog.Logger

	mu   sync.Mutex
	held map[string]string // channelID -> runtime token

	// reconcile is called when a held lease is lost or a claimable channel
	// appears — the host's per-channel reconcile re-evaluates ownership.
	reconcile func(ctx context.Context, channelID string)
	// runtimeErrored reports a channel's runtime error snapshot (async Start
	// failures land there after Apply returns nil).
	runtimeErrored func(channelID string) bool
}

func NewChannelLeases(db *pgxpool.Pool, ownerID string) *ChannelLeases {
	return &ChannelLeases{
		q:       sqlc.New(db),
		ownerID: ownerID,
		log:     slog.With("component", "channel_leases", "owner", ownerID),
		held:    map[string]string{},
	}
}

func (t *ChannelLeases) bindReconcile(fn func(context.Context, string)) { t.reconcile = fn }

// bindRuntimeErrored lets the renew loop see asynchronously failed starts:
// a runtime sitting in an error snapshot is re-evaluated instead of renewing
// a dead owner forever.
func (t *ChannelLeases) bindRuntimeErrored(fn func(string) bool) { t.runtimeErrored = fn }

// Ensure returns the claimed channel row when this replica holds (or just
// acquired) the lease, plus true. A false return means the channel must not
// run here — either another replica owns it or the DB is unreachable, in
// which case we fail closed.
func (t *ChannelLeases) Ensure(ctx context.Context, channelID string) (sqlc.Channel, bool) {
	t.mu.Lock()
	token, ok := t.held[channelID]
	if !ok {
		token = uuid.Must(uuid.NewV7()).String()
	}
	t.mu.Unlock()

	row, err := t.q.ClaimChannelRuntime(ctx, sqlc.ClaimChannelRuntimeParams{
		ChannelID: channelID,
		OwnerID:   pgtype.Text{String: t.ownerID, Valid: true},
		Token:     pgtype.Text{String: token, Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// Another replica owns a live lease.
		t.drop(channelID)
		return sqlc.Channel{}, false
	}
	if err != nil {
		t.log.WarnContext(ctx, "channel lease claim failed; failing closed", "channel", channelID, "error", err)
		return sqlc.Channel{}, false
	}
	t.mu.Lock()
	t.held[channelID] = row.RuntimeToken.String
	t.mu.Unlock()
	return row, true
}

// Token returns the fencing token for a held lease ("" when not held). Send
// and checkpoint paths stamp it into owner_token.
func (t *ChannelLeases) Token(channelID string) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.held[channelID]
}

func (t *ChannelLeases) drop(channelID string) {
	t.mu.Lock()
	delete(t.held, channelID)
	t.mu.Unlock()
}

// Release frees a held lease (channel disabled, deleted, or drained).
func (t *ChannelLeases) Release(ctx context.Context, channelID string) {
	t.mu.Lock()
	token, ok := t.held[channelID]
	delete(t.held, channelID)
	t.mu.Unlock()
	if !ok {
		return
	}
	if _, err := t.q.ReleaseChannelRuntime(ctx, sqlc.ReleaseChannelRuntimeParams{ChannelID: channelID, Token: pgtype.Text{String: token, Valid: true}}); err != nil {
		t.log.WarnContext(ctx, "channel lease release failed", "channel", channelID, "error", err)
	}
}

// MarkApplied records the owner's observed state and applied config revision,
// fenced by the held token. Best-effort: a fenced-out write just reports false.
func (t *ChannelLeases) MarkApplied(ctx context.Context, channelID, state, errCode string, appliedRevision int64) bool {
	t.mu.Lock()
	token, ok := t.held[channelID]
	t.mu.Unlock()
	if !ok {
		return false
	}
	n, err := t.q.UpdateChannelRuntimeState(ctx, sqlc.UpdateChannelRuntimeStateParams{
		ChannelID:       channelID,
		Token:           pgtype.Text{String: token, Valid: true},
		State:           state,
		ErrorCode:       pgtype.Text{String: errCode, Valid: errCode != ""},
		AppliedRevision: pgtype.Int8{Int64: appliedRevision, Valid: true},
	})
	return err == nil && n == 1
}

// Run renews held leases and sweeps claimable channels until ctx ends. Lease
// loss or a newly claimable channel triggers reconcile, which re-evaluates
// ownership through Ensure and stops or starts the runtime accordingly.
func (t *ChannelLeases) Run(ctx context.Context) {
	renew := time.NewTicker(channelLeaseRenewInterval)
	defer renew.Stop()
	sweep := time.NewTicker(channelLeaseSweepInterval)
	defer sweep.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-renew.C:
			t.renewHeld(ctx)
		case <-sweep.C:
			t.sweep(ctx)
		}
	}
}

func (t *ChannelLeases) renewHeld(ctx context.Context) {
	t.mu.Lock()
	pairs := make(map[string]string, len(t.held))
	maps.Copy(pairs, t.held)
	t.mu.Unlock()
	for id, token := range pairs {
		row, err := t.q.RenewChannelRuntime(ctx, sqlc.RenewChannelRuntimeParams{ChannelID: id, Token: pgtype.Text{String: token, Valid: true}})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				// Lease already moved — drop and reconcile immediately.
				t.drop(id)
				t.log.InfoContext(ctx, "channel lease lost", "channel", id)
				if t.reconcile != nil {
					t.reconcile(ctx, id)
				}
				continue
			}
			// Fail closed: an unverifiable token must not keep sending AND
			// must not keep receiving. Dropping the token blocks sends; the
			// reconcile re-evaluates ownership — another owner's live lease or
			// an unreachable DB both stop the local poller. A transient error
			// costs one restart, which is cheaper than a zombie ingress.
			t.drop(id)
			if ctx.Err() == nil {
				t.log.WarnContext(ctx, "channel lease renew failed", "channel", id, "error", err)
			}
			if t.reconcile != nil {
				t.reconcile(ctx, id)
			}
			continue
		}
		// Desired-state drift: disabled or a newer config_revision than we
		// applied means our running runtime is stale — re-evaluate now.
		if !row.Enabled || !row.AppliedRevision.Valid || row.ConfigRevision != row.AppliedRevision.Int64 {
			if t.reconcile != nil {
				t.reconcile(ctx, id)
			}
			continue
		}
		// Async start failure: the runtime's snapshot went error after Apply
		// returned. Reconcile re-applies (a retry when Ensure still wins) or
		// releases the lease through applyChannel's failure path.
		if t.runtimeErrored != nil && t.runtimeErrored(id) {
			if t.reconcile != nil {
				t.reconcile(ctx, id)
			}
		}
	}
}

func (t *ChannelLeases) sweep(ctx context.Context) {
	rows, err := t.q.ListClaimableChannels(ctx)
	if err != nil {
		if ctx.Err() == nil {
			t.log.WarnContext(ctx, "channel lease sweep failed", "error", err)
		}
		return
	}
	for _, ch := range rows {
		if t.reconcile != nil {
			t.reconcile(ctx, ch.ID)
		}
	}
}

// WithChannelLeases makes durable channel start conditional on the DB lease:
// this replica reconciles enabled channels only while it holds the token.
// Pass nil db to keep the legacy unconditional-start behavior.
func WithChannelLeases(db *pgxpool.Pool, ownerID string) Option {
	return func(h *Host) {
		if db == nil {
			return
		}
		leases := NewChannelLeases(db, ownerID)
		leases.bindReconcile(func(ctx context.Context, channelID string) {
			if err := h.runtimes.ReconcileChannel(ctx, channelID); err != nil {
				h.log.WarnContext(ctx, "lease-triggered reconcile failed", "channel", channelID, "error", err)
			}
		})
		leases.bindRuntimeErrored(h.runtimes.ChannelRuntimeErrored)
		h.runtimes.channelLeases = leases
		h.channelLeases = leases
	}
}
