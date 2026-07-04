package server

import (
	"net/http"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	apiserver "github.com/CherryHQ/stella/api/server"
	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/goal"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

const inboxRecentFailureWindow = 7 * 24 * time.Hour

func (s *Server) ListInbox(w http.ResponseWriter, r *http.Request, params apiserver.ListInboxParams) {
	info := requireAuth(w, r)
	if info == nil {
		return
	}

	pageSize := 20
	if params.PageSize != nil {
		if *params.PageSize < 0 {
			writeError(w, http.StatusBadRequest, "page_size must not be negative")
			return
		}
		if *params.PageSize > 0 {
			pageSize = *params.PageSize
		}
	}
	if pageSize > 100 {
		pageSize = 100
	}
	offset, err := decodeOffsetToken(derefStr(params.PageToken))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid page token")
		return
	}

	limit := int32(offset + pageSize + 1)
	agentID := nullableStringParam(params.AgentId)
	since := time.Now().UTC().Add(-inboxRecentFailureWindow)
	ctx := r.Context()

	goals, err := s.q.ListInboxGoals(ctx, sqlc.ListInboxGoalsParams{
		UserID:     info.UserID,
		AgentID:    agentID,
		Since:      since,
		LimitCount: limit,
	})
	if err != nil {
		s.writeInternalError(w, err)
		return
	}
	schedulerRuns, err := s.q.ListFailedInboxSchedulerRuns(ctx, sqlc.ListFailedInboxSchedulerRunsParams{
		UserID:     pgtype.Text{String: info.UserID, Valid: true},
		Since:      pgtype.Timestamptz{Time: since, Valid: true},
		AgentID:    agentID,
		LimitCount: limit,
	})
	if err != nil {
		s.writeInternalError(w, err)
		return
	}

	items := make([]apitypes.InboxItem, 0, len(goals)+len(schedulerRuns))
	for _, row := range goals {
		items = append(items, goalInboxItem(row))
	}
	for _, row := range schedulerRuns {
		items = append(items, failedSchedulerRunInboxItem(row))
	}

	sort.SliceStable(items, func(i, j int) bool {
		if items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].Id > items[j].Id
		}
		return items[i].CreatedAt.After(items[j].CreatedAt)
	})

	var nextPageToken *string
	if offset < len(items) {
		items = items[offset:]
	} else {
		// Keep a non-nil slice: the spec requires items to be an array.
		items = items[:0]
	}
	if len(items) > pageSize {
		items = items[:pageSize]
		tok := encodeOffsetToken(offset + pageSize)
		nextPageToken = &tok
	}

	writeData(w, http.StatusOK, apitypes.InboxList{
		Items:         items,
		NextPageToken: nextPageToken,
	})
}

func nullableStringParam(value *string) pgtype.Text {
	if value == nil || *value == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: *value, Valid: true}
}

// goalInboxItem renders a blocked or terminally-failed goal as an
// inbox entry. The kind and detail are derived from lifecycle/block_reason — the
// goal model has no per-block message, so the reason IS the detail.
func goalInboxItem(row sqlc.ListInboxGoalsRow) apitypes.InboxItem {
	kind, prefix, detail := inboxFacetForGoal(row.Lifecycle, row.BlockReason)
	return apitypes.InboxItem{
		Id:         prefix + ":" + row.ID,
		Kind:       kind,
		Title:      row.Title,
		Detail:     &detail,
		AgentId:    &row.AgentID,
		ProjectId:  nullableStringPtr(row.ProjectID),
		SourceType: apitypes.InboxItemSourceTypeGoal,
		SourceId:   row.ID,
		TargetPath: goalTargetPath(row.AgentID, row.ID),
		CreatedAt:  row.UpdatedAt.UTC(),
	}
}

// inboxFacetForGoal maps a goal's lifecycle/block_reason to its inbox kind, id
// prefix, and a human-readable detail line.
func inboxFacetForGoal(lifecycle, blockReason string) (apitypes.InboxItemKind, string, string) {
	if lifecycle == goal.LifecycleBlocked {
		switch blockReason {
		case goal.BlockNeedsVerdict:
			return apitypes.InboxItemKindReview, "review", "Awaiting your verdict"
		case goal.BlockNeedsPlanApproval:
			return apitypes.InboxItemKindReview, "review", "Plan awaiting your approval"
		case goal.BlockBudgetExhausted:
			return apitypes.InboxItemKindBlocked, "blocked", "Attempt budget exhausted"
		case goal.BlockPlanningInvalid:
			return apitypes.InboxItemKindBlocked, "blocked", "Planning failed"
		case goal.BlockContractConflict:
			return apitypes.InboxItemKindBlocked, "blocked", "Acceptance contract conflict"
		case goal.BlockEnvUnavailable:
			return apitypes.InboxItemKindBlocked, "blocked", "Environment unavailable"
		default:
			return apitypes.InboxItemKindBlocked, "blocked", "Blocked"
		}
	}
	return apitypes.InboxItemKindFailed, "failed", "Failed with no retry path left"
}

func failedSchedulerRunInboxItem(row sqlc.ListFailedInboxSchedulerRunsRow) apitypes.InboxItem {
	createdAt := parseTimePtr(row.FinishedAt)
	if createdAt == nil {
		t := row.StartedAt.UTC()
		createdAt = &t
	}
	return apitypes.InboxItem{
		Id:         "scheduler-run:" + row.RunID,
		Kind:       apitypes.InboxItemKindFailed,
		Title:      row.Name,
		Detail:     optionalString(row.Error),
		AgentId:    nullableStringPtr(row.AgentID),
		SourceType: apitypes.InboxItemSourceTypeSchedulerRun,
		SourceId:   row.RunID,
		TargetPath: schedulerRunTargetPath(row),
		CreatedAt:  *createdAt,
	}
}

func nullableStringPtr(ns pgtype.Text) *string {
	if !ns.Valid || ns.String == "" {
		return nil
	}
	return &ns.String
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func goalTargetPath(agentID, goalID string) string {
	return "/agents/" + agentID + "/goals/" + goalID
}

func schedulerRunTargetPath(row sqlc.ListFailedInboxSchedulerRunsRow) string {
	if row.AgentID.Valid {
		return "/agents/" + row.AgentID.String + "/goals/schedules/" + row.JobID
	}
	return "/agents"
}
