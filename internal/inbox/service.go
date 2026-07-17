// Package inbox is the cross-Goal/Scheduler inbox read model: it owns the two
// candidate queries (blocked/failed goals and failed scheduler runs), merges
// them, applies a stable recency sort, and paginates, returning transport-neutral
// domain items. It is a deep read model — the HTTP transport hands it a trusted
// authz.Authority and an agent filter and receives ready-to-render items with no
// query, sql row, or goal-internal knowledge of its own.
package inbox

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/goal"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// recentFailureWindow bounds how far back a failed scheduler run or terminal goal
// stays surfaced in the inbox.
const recentFailureWindow = 7 * 24 * time.Hour

var (
	// ErrForbidden reports an unauthenticated or non-user actor (the inbox is
	// scoped to the authenticated user's own items). It maps to 403.
	ErrForbidden = errors.New("inbox access forbidden")
	// ErrInvalidPage rejects a page window that cannot be represented by the
	// int32 SQL limit, rather than letting a crafted token wrap to a negative
	// PostgreSQL LIMIT.
	ErrInvalidPage = errors.New("invalid inbox page")
)

// Kind classifies an inbox item's actionability.
type Kind string

const (
	KindReview  Kind = "review"
	KindBlocked Kind = "blocked"
	KindFailed  Kind = "failed"
)

// Source identifies which subsystem produced an item.
type Source string

const (
	SourceGoal         Source = "goal"
	SourceSchedulerRun Source = "scheduler_run"
)

// Item is the transport-neutral inbox entry. AgentID/ProjectID/Detail are plain
// strings ("" when absent); the transport maps them to the API's optional fields.
type Item struct {
	ID         string
	Kind       Kind
	Title      string
	Detail     string
	AgentID    string
	ProjectID  string
	Source     Source
	SourceID   string
	TargetPath string
	CreatedAt  time.Time
}

// candidateQueries is the narrow query surface the inbox read model needs. It is
// an interface so the merge/sort/page/filter behavior is testable without a live
// database; *sqlc.Queries satisfies it.
type candidateQueries interface {
	ListInboxGoals(ctx context.Context, arg sqlc.ListInboxGoalsParams) ([]sqlc.ListInboxGoalsRow, error)
	ListFailedInboxSchedulerRuns(ctx context.Context, arg sqlc.ListFailedInboxSchedulerRunsParams) ([]sqlc.ListFailedInboxSchedulerRunsRow, error)
}

// Service owns the inbox queries and read-model assembly.
type Service struct {
	q candidateQueries
}

// NewService builds the inbox read model over the given pool.
func NewService(db *pgxpool.Pool) *Service {
	return &Service{q: sqlc.New(db)}
}

// List returns the caller's inbox items, merged from blocked/failed goals and
// failed scheduler runs, stably sorted newest-first (id-desc tiebreak), then
// paginated by (offset, pageSize). hasMore reports whether a further page exists.
// It scopes to the caller's own items and fails closed for a non-user actor
// before any query. agentFilter ("" = all) narrows both sources to one agent.
func (s *Service) List(ctx context.Context, authority authz.Authority, agentFilter string, offset, pageSize int) ([]Item, bool, error) {
	if !authority.Valid() || authority.Kind() != authz.ActorUser {
		return nil, false, ErrForbidden
	}
	userID := string(authority.UserID())
	since := time.Now().UTC().Add(-recentFailureWindow)
	// Fetch enough from each source to satisfy this page after the merge: the
	// offset already consumed, this page, and one probe row for hasMore. Reject a
	// crafted page token that would overflow sqlc's int32 LIMIT.
	const maxInt32 = int64(1<<31 - 1)
	window := int64(offset) + int64(pageSize) + 1
	if offset < 0 || pageSize <= 0 || window > maxInt32 {
		return nil, false, ErrInvalidPage
	}
	limit := int32(window)
	agentText := textFilter(agentFilter)

	goals, err := s.q.ListInboxGoals(ctx, sqlc.ListInboxGoalsParams{
		UserID: userID, AgentID: agentText, Since: since, LimitCount: limit,
	})
	if err != nil {
		return nil, false, err
	}
	runs, err := s.q.ListFailedInboxSchedulerRuns(ctx, sqlc.ListFailedInboxSchedulerRunsParams{
		UserID:     pgtype.Text{String: userID, Valid: true},
		Since:      pgtype.Timestamptz{Time: since, Valid: true},
		AgentID:    agentText,
		LimitCount: limit,
	})
	if err != nil {
		return nil, false, err
	}

	items := make([]Item, 0, len(goals)+len(runs))
	for _, row := range goals {
		items = append(items, goalItem(row))
	}
	for _, row := range runs {
		items = append(items, schedulerRunItem(row))
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].ID > items[j].ID
		}
		return items[i].CreatedAt.After(items[j].CreatedAt)
	})

	if offset < len(items) {
		items = items[offset:]
	} else {
		items = items[:0]
	}
	hasMore := len(items) > pageSize
	if hasMore {
		items = items[:pageSize]
	}
	return items, hasMore, nil
}

// goalItem renders a blocked or terminally-failed goal. The goal model has no
// per-block message, so the block reason IS the detail.
func goalItem(row sqlc.ListInboxGoalsRow) Item {
	kind, prefix, detail := facetForGoal(row.Lifecycle, row.BlockReason)
	return Item{
		ID:         prefix + ":" + row.ID,
		Kind:       kind,
		Title:      row.Title,
		Detail:     detail,
		AgentID:    row.AgentID,
		ProjectID:  textValue(row.ProjectID),
		Source:     SourceGoal,
		SourceID:   row.ID,
		TargetPath: "/agents/" + row.AgentID + "/goals/" + row.ID,
		CreatedAt:  row.UpdatedAt.UTC(),
	}
}

// facetForGoal maps a goal's lifecycle/block_reason to its inbox kind, id prefix,
// and a human-readable detail line.
func facetForGoal(lifecycle, blockReason string) (Kind, string, string) {
	if lifecycle == goal.LifecycleBlocked {
		switch blockReason {
		case goal.BlockNeedsVerdict:
			return KindReview, "review", "Awaiting your verdict"
		case goal.BlockNeedsPlanApproval:
			return KindReview, "review", "Plan awaiting your approval"
		case goal.BlockBudgetExhausted:
			return KindBlocked, "blocked", "Attempt budget exhausted"
		case goal.BlockPlanningInvalid:
			return KindBlocked, "blocked", "Planning failed"
		case goal.BlockContractConflict:
			return KindBlocked, "blocked", "Acceptance contract conflict"
		case goal.BlockEnvUnavailable:
			return KindBlocked, "blocked", "Environment unavailable"
		default:
			return KindBlocked, "blocked", "Blocked"
		}
	}
	return KindFailed, "failed", "Failed with no retry path left"
}

// schedulerRunItem renders a failed scheduler run. Its recency time is the finish
// time when present, else the start time.
func schedulerRunItem(row sqlc.ListFailedInboxSchedulerRunsRow) Item {
	createdAt := row.StartedAt.UTC()
	if row.FinishedAt.Valid && !row.FinishedAt.Time.IsZero() {
		createdAt = row.FinishedAt.Time.UTC()
	}
	return Item{
		ID:         "scheduler-run:" + row.RunID,
		Kind:       KindFailed,
		Title:      row.Name,
		Detail:     row.Error,
		AgentID:    textValue(row.AgentID),
		Source:     SourceSchedulerRun,
		SourceID:   row.RunID,
		TargetPath: schedulerRunTargetPath(row),
		CreatedAt:  createdAt,
	}
}

func schedulerRunTargetPath(row sqlc.ListFailedInboxSchedulerRunsRow) string {
	if row.AgentID.Valid {
		return "/agents/" + row.AgentID.String + "/goals/schedules/" + row.JobID
	}
	return "/agents"
}

func textFilter(agentID string) pgtype.Text {
	if agentID == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: agentID, Valid: true}
}

func textValue(v pgtype.Text) string {
	if !v.Valid {
		return ""
	}
	return v.String
}
