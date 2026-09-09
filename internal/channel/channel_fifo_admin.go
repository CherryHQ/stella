package channel

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

var (
	// ErrChannelFIFOAdminNotFound means the exact operator-supplied item ID does
	// not identify a durable FIFO row.
	ErrChannelFIFOAdminNotFound = errors.New("channel FIFO item not found")
	// ErrChannelFIFOAdminForceRequired keeps the destructive operator action
	// explicit in both the service API and the CLI.
	ErrChannelFIFOAdminForceRequired = errors.New("channel FIFO rejection requires explicit force")
	// ErrChannelFIFOAdminNotBlocked prevents an operator from taking ownership of
	// a pending or running item. Only the worker may settle those states.
	ErrChannelFIFOAdminNotBlocked = errors.New("channel FIFO item is not blocked")
	// ErrChannelFIFOAdminLiveRun fails closed while a linked AgentRun is still
	// running. The maintenance service has no executor lease authority.
	ErrChannelFIFOAdminLiveRun = errors.New("channel FIFO item still has a live AgentRun")
)

// FIFOAdminRecord is the operator-facing view of one exact FIFO item. Payload
// is retained in Item for forensic inspection; list-style diagnostics should
// use the smaller fields directly instead of serializing this record in bulk.
type FIFOAdminRecord struct {
	Item       sqlc.ChannelFifoItem        `json:"item"`
	Binding    sqlc.ChannelBinding         `json:"binding"`
	Run        *sqlc.AgentRun              `json:"run,omitempty"`
	Rejections []sqlc.ChannelFifoRejection `json:"rejections,omitempty"`
}

const DefaultFIFOAdminListLimit = 100

// FIFOAdminSummary is the bounded, payload-free projection used by list. The
// exact item ID can then be passed to Inspect when the retained payload is
// actually needed.
type FIFOAdminSummary struct {
	ID            string    `json:"id"`
	BindingID     string    `json:"binding_id"`
	PrincipalKey  string    `json:"principal_key"`
	Seq           int64     `json:"seq"`
	SourceKey     string    `json:"source_key"`
	Command       string    `json:"command"`
	State         string    `json:"state"`
	Attempt       int32     `json:"attempt"`
	RunID         string    `json:"run_id,omitempty"`
	NextAttemptAt time.Time `json:"next_attempt_at"`
	ErrorCode     string    `json:"error_code"`
	ErrorDetail   string    `json:"error_detail"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func ValidateFIFOAdminListLimit(limit int) error {
	if limit < 1 || limit > DefaultFIFOAdminListLimit {
		return fmt.Errorf("fifo list --limit must be between 1 and %d", DefaultFIFOAdminListLimit)
	}
	return nil
}

// FIFOAdminRejectResult reports whether this invocation performed the terminal
// transition. A terminal receipt is idempotent: it is returned with Changed
// false after cleanup, without releasing quota a second time or adding a
// duplicate audit row.
type FIFOAdminRejectResult struct {
	Record  FIFOAdminRecord            `json:"record"`
	Audit   *sqlc.ChannelFifoRejection `json:"audit,omitempty"`
	Changed bool                       `json:"changed"`
}

// FIFOAdmin owns the database-only operator path for durable channel FIFO
// recovery. It does not claim a worker lease, start an executor, or run a
// publisher.
type FIFOAdmin struct {
	db *pgxpool.Pool
}

// NewFIFOAdmin constructs a maintenance service over an already opened pool.
// Callers that run outside stellad startup are responsible for opening a
// maintenance pool without applying migrations.
func NewFIFOAdmin(db *pgxpool.Pool) *FIFOAdmin {
	return &FIFOAdmin{db: db}
}

// Inspect reads one exact item and its binding, linked AgentRun, and rejection
// audit. It never returns payloads from a collection query, so the exact ID is
// the only way to ask for potentially large retained content.
func (a *FIFOAdmin) Inspect(ctx context.Context, id string) (FIFOAdminRecord, error) {
	if a == nil || a.db == nil {
		return FIFOAdminRecord{}, errors.New("channel FIFO admin is not configured")
	}
	id, err := normalizeFIFOAdminID(id)
	if err != nil {
		return FIFOAdminRecord{}, err
	}
	q := sqlc.New(a.db)
	item, err := q.GetChannelFIFOItem(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return FIFOAdminRecord{}, fmt.Errorf("%w: %s", ErrChannelFIFOAdminNotFound, id)
	}
	if err != nil {
		return FIFOAdminRecord{}, fmt.Errorf("inspect channel FIFO item %s: %w", id, err)
	}
	return inspectFIFOAdminRecord(ctx, q, item)
}

// ListBlocked returns only unreleased blocked-item summaries. It never scans
// or serializes payload, so the operator can discover exact IDs safely.
func (a *FIFOAdmin) ListBlocked(ctx context.Context, limit int) ([]FIFOAdminSummary, error) {
	if a == nil || a.db == nil {
		return nil, errors.New("channel FIFO admin is not configured")
	}
	if limit == 0 {
		limit = DefaultFIFOAdminListLimit
	}
	if err := ValidateFIFOAdminListLimit(limit); err != nil {
		return nil, err
	}
	rows, err := sqlc.New(a.db).ListBlockedChannelFIFOSummaries(ctx, int32(limit))
	if err != nil {
		return nil, fmt.Errorf("list blocked channel FIFO items: %w", err)
	}
	summaries := make([]FIFOAdminSummary, 0, len(rows))
	for _, row := range rows {
		runID := ""
		if row.RunID.Valid {
			runID = row.RunID.String
		}
		summaries = append(summaries, FIFOAdminSummary{
			ID: row.ID, BindingID: row.BindingID, PrincipalKey: row.PrincipalKey,
			Seq: row.Seq, SourceKey: row.SourceKey, Command: row.Command,
			State: row.State, Attempt: row.Attempt, RunID: runID,
			NextAttemptAt: row.NextAttemptAt, ErrorCode: row.ErrorCode,
			ErrorDetail: row.ErrorDetail, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		})
	}
	return summaries, nil
}

// Reject performs the attributed terminal transition for one blocked item.
// The transition intentionally does not accept a lease owner: operators cannot
// impersonate a worker and cannot reject a pending/running item. A linked run
// may be terminal (the recovery worker already converted the item to blocked),
// but a running run is always a hard stop.
func (a *FIFOAdmin) Reject(ctx context.Context, id, operator, reason string, force bool) (FIFOAdminRejectResult, error) {
	if a == nil || a.db == nil {
		return FIFOAdminRejectResult{}, errors.New("channel FIFO admin is not configured")
	}
	if !force {
		return FIFOAdminRejectResult{}, ErrChannelFIFOAdminForceRequired
	}
	id, err := normalizeFIFOAdminID(id)
	if err != nil {
		return FIFOAdminRejectResult{}, err
	}
	operator = strings.TrimSpace(operator)
	if operator == "" {
		return FIFOAdminRejectResult{}, errors.New("channel FIFO operator is required")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return FIFOAdminRejectResult{}, errors.New("channel FIFO rejection reason is required")
	}

	tx, err := a.db.Begin(ctx)
	if err != nil {
		return FIFOAdminRejectResult{}, fmt.Errorf("begin channel FIFO rejection: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := sqlc.New(tx)

	// Read immutable routing/quota coordinates before taking locks. The actual
	// item is re-read with FOR UPDATE after the shared quota locks, preserving
	// deployment -> principal -> binding -> item order used by admission and
	// worker terminalization.
	snapshot, err := q.GetChannelFIFOItem(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return FIFOAdminRejectResult{}, fmt.Errorf("%w: %s", ErrChannelFIFOAdminNotFound, id)
	}
	if err != nil {
		return FIFOAdminRejectResult{}, fmt.Errorf("load channel FIFO item %s: %w", id, err)
	}
	if snapshot.PrincipalKey == "" || snapshot.BindingID == "" {
		return FIFOAdminRejectResult{}, errors.New("channel FIFO item has incomplete quota ownership")
	}
	if _, err := q.GetChannelDeploymentQuotaForUpdate(ctx, channelDeploymentQuotaKey); err != nil {
		return FIFOAdminRejectResult{}, fmt.Errorf("lock channel deployment quota: %w", err)
	}
	if _, err := q.GetChannelPrincipalQuotaForUpdate(ctx, snapshot.PrincipalKey); err != nil {
		return FIFOAdminRejectResult{}, fmt.Errorf("lock channel principal quota: %w", err)
	}
	binding, err := q.GetChannelBindingForUpdate(ctx, snapshot.BindingID)
	if err != nil {
		return FIFOAdminRejectResult{}, fmt.Errorf("lock channel binding: %w", err)
	}
	item, err := q.GetChannelFIFOItemForUpdate(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return FIFOAdminRejectResult{}, fmt.Errorf("%w: %s", ErrChannelFIFOAdminNotFound, id)
	}
	if err != nil {
		return FIFOAdminRejectResult{}, fmt.Errorf("lock channel FIFO item %s: %w", id, err)
	}
	if item.BindingID != snapshot.BindingID || item.PrincipalKey != snapshot.PrincipalKey || binding.ID != item.BindingID {
		return FIFOAdminRejectResult{}, errors.New("channel FIFO quota ownership changed while locking item")
	}

	// AgentRun transitions are monotonic from running to terminal. A plain read
	// is deliberate here: locking run after quota/item would deadlock with the
	// executor path, which terminalizes the run before it acquires the same
	// quota locks. Holding the item lock prevents a concurrent FIFO transition;
	// a running run is rejected conservatively if it races this read.
	var run *sqlc.AgentRun
	if item.RunID.Valid {
		runRow, runErr := q.GetAgentRun(ctx, item.RunID.String)
		if errors.Is(runErr, pgx.ErrNoRows) {
			return FIFOAdminRejectResult{}, fmt.Errorf("channel FIFO item %s references missing AgentRun %s", id, item.RunID.String)
		}
		if runErr != nil {
			return FIFOAdminRejectResult{}, fmt.Errorf("inspect AgentRun %s: %w", item.RunID.String, runErr)
		}
		run = &runRow
		if run.Status == "running" {
			return FIFOAdminRejectResult{}, fmt.Errorf("%w: %s", ErrChannelFIFOAdminLiveRun, run.ID)
		}
	}

	// A terminal row is an idempotent receipt. It may have been terminalized by
	// the worker before media cleanup was added, so run the same cleanup here;
	// quota counters are already released and must never be incremented again.
	if fifoItemTerminal(item) {
		if err := cleanupTerminalFIFOItem(ctx, q, item.ID); err != nil {
			return FIFOAdminRejectResult{}, err
		}
		record, err := inspectFIFOAdminRecordWithBinding(ctx, q, item, binding, run)
		if err != nil {
			return FIFOAdminRejectResult{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return FIFOAdminRejectResult{}, fmt.Errorf("commit channel FIFO terminal cleanup: %w", err)
		}
		return FIFOAdminRejectResult{Record: record}, nil
	}
	if item.State != "blocked" || item.ReleasedAt.Valid {
		return FIFOAdminRejectResult{}, fmt.Errorf("%w: state=%s", ErrChannelFIFOAdminNotBlocked, item.State)
	}

	rejected, err := q.RejectBlockedChannelFIFOItem(ctx, sqlc.RejectBlockedChannelFIFOItemParams{
		ID: id, RejectedBy: operator, RejectedReason: reason,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return FIFOAdminRejectResult{}, fmt.Errorf("%w: state changed while rejecting %s", ErrChannelFIFOAdminNotBlocked, id)
	}
	if err != nil {
		return FIFOAdminRejectResult{}, fmt.Errorf("reject channel FIFO item %s: %w", id, err)
	}
	audit, err := q.CreateChannelFIFORejection(ctx, sqlc.CreateChannelFIFORejectionParams{
		ItemID: id, OperatorID: operator, Reason: reason,
	})
	if err != nil {
		return FIFOAdminRejectResult{}, fmt.Errorf("audit channel FIFO rejection %s: %w", id, err)
	}
	if err := releaseRejectedFIFOQuota(ctx, q, rejected); err != nil {
		return FIFOAdminRejectResult{}, err
	}
	if err := cleanupTerminalFIFOItem(ctx, q, rejected.ID); err != nil {
		return FIFOAdminRejectResult{}, err
	}
	record, err := inspectFIFOAdminRecordWithBinding(ctx, q, rejected, binding, run)
	if err != nil {
		return FIFOAdminRejectResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return FIFOAdminRejectResult{}, fmt.Errorf("commit channel FIFO rejection %s: %w", id, err)
	}
	return FIFOAdminRejectResult{Record: record, Audit: &audit, Changed: true}, nil
}

func normalizeFIFOAdminID(id string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", errors.New("channel FIFO item ID is required")
	}
	parsed, err := uuid.Parse(id)
	if err != nil {
		return "", fmt.Errorf("channel FIFO item ID must be a UUID: %w", err)
	}
	return parsed.String(), nil
}

func fifoItemTerminal(item sqlc.ChannelFifoItem) bool {
	return item.ReleasedAt.Valid || item.State == "completed" || item.State == "rejected" || item.State == "discarded"
}

func inspectFIFOAdminRecord(ctx context.Context, q *sqlc.Queries, item sqlc.ChannelFifoItem) (FIFOAdminRecord, error) {
	binding, err := q.GetChannelBinding(ctx, item.BindingID)
	if err != nil {
		return FIFOAdminRecord{}, fmt.Errorf("inspect channel binding %s: %w", item.BindingID, err)
	}
	var run *sqlc.AgentRun
	if item.RunID.Valid {
		runRow, runErr := q.GetAgentRun(ctx, item.RunID.String)
		if runErr != nil {
			return FIFOAdminRecord{}, fmt.Errorf("inspect AgentRun %s: %w", item.RunID.String, runErr)
		}
		run = &runRow
	}
	return inspectFIFOAdminRecordWithBinding(ctx, q, item, binding, run)
}

func inspectFIFOAdminRecordWithBinding(ctx context.Context, q *sqlc.Queries, item sqlc.ChannelFifoItem, binding sqlc.ChannelBinding, run *sqlc.AgentRun) (FIFOAdminRecord, error) {
	rejections, err := q.ListChannelFIFORejections(ctx, item.ID)
	if err != nil {
		return FIFOAdminRecord{}, fmt.Errorf("inspect channel FIFO rejection audit %s: %w", item.ID, err)
	}
	return FIFOAdminRecord{Item: item, Binding: binding, Run: run, Rejections: rejections}, nil
}

func releaseRejectedFIFOQuota(ctx context.Context, q *sqlc.Queries, item sqlc.ChannelFifoItem) error {
	rows, err := q.ReleaseChannelDeploymentQuota(ctx, sqlc.ReleaseChannelDeploymentQuotaParams{
		ChannelID: channelDeploymentQuotaKey, ByteCost: item.ByteCost,
	})
	if err != nil {
		return fmt.Errorf("release channel deployment quota: %w", err)
	}
	if rows != 1 {
		return errors.New("channel deployment quota release lost its row")
	}
	rows, err = q.ReleaseChannelPrincipalQuota(ctx, sqlc.ReleaseChannelPrincipalQuotaParams{
		PrincipalKey: item.PrincipalKey, ByteCost: item.ByteCost,
	})
	if err != nil {
		return fmt.Errorf("release channel principal quota: %w", err)
	}
	if rows != 1 {
		return errors.New("channel principal quota release lost its row")
	}
	rows, err = q.ReleaseChannelBindingQuota(ctx, sqlc.ReleaseChannelBindingQuotaParams{
		BindingID: item.BindingID, ByteCost: item.ByteCost,
	})
	if err != nil {
		return fmt.Errorf("release channel binding quota: %w", err)
	}
	if rows != 1 {
		return errors.New("channel binding quota release lost its row")
	}
	return nil
}

func cleanupTerminalFIFOItem(ctx context.Context, q *sqlc.Queries, itemID string) error {
	if _, err := q.DeleteChannelReplyCapabilityForItem(ctx, itemID); err != nil {
		return fmt.Errorf("delete channel FIFO reply capability %s: %w", itemID, err)
	}
	if _, err := q.DeleteChannelFIFOMediaForItem(ctx, itemID); err != nil {
		return fmt.Errorf("delete channel FIFO media references %s: %w", itemID, err)
	}
	return nil
}
