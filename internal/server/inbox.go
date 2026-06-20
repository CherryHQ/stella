package server

import (
	"database/sql"
	"net/http"
	"sort"
	"time"

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
		UserID:     sql.NullString{String: info.UserID, Valid: true},
		Since:      sql.NullTime{Time: since, Valid: true},
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

func nullableStringParam(value *string) sql.NullString {
	if value == nil || *value == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: *value, Valid: true}
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

// inboxFacetForGoal maps a goal's lifecycle/block_reason to its
// inbox kind, id prefix, and a human-readable detail line.
func inboxFacetForGoal(lifecycle, blockReason string) (apitypes.InboxItemKind, string, string) {
	switch {
	case lifecycle == goal.LifecycleBlocked && blockReason == goal.BlockNeedsVerdict:
		return apitypes.InboxItemKindReview, "review", "Awaiting your verdict"
	case lifecycle == goal.LifecycleBlocked && blockReason == goal.BlockBudgetExhausted:
		return apitypes.InboxItemKindBlocked, "blocked", "Attempt budget exhausted"
	case lifecycle == goal.LifecycleBlocked:
		return apitypes.InboxItemKindBlocked, "blocked", "Blocked on a dependency"
	case lifecycle == goal.LifecycleAbandoned:
		return apitypes.InboxItemKindFailed, "failed", "Abandoned after budget exhaustion"
	default: // rejected_final
		return apitypes.InboxItemKindFailed, "failed", "Rejected with no rework path left"
	}
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

func nullableStringPtr(ns sql.NullString) *string {
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
		return "/agents/" + row.AgentID.String + "/tasks"
	}
	return "/agents"
}
