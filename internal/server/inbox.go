package server

import (
	"database/sql"
	"net/http"
	"sort"
	"time"

	apiserver "github.com/CherryHQ/stella/api/server"
	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/deliverable"
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

	limit := int64(offset + pageSize + 1)
	agentID := nullableStringParam(params.AgentId)
	since := time.Now().UTC().Add(-inboxRecentFailureWindow).Format("2006-01-02 15:04:05")
	ctx := r.Context()

	deliverables, err := s.q.ListInboxDeliverables(ctx, sqlc.ListInboxDeliverablesParams{
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
		Since:      sql.NullString{String: since, Valid: true},
		AgentID:    agentID,
		LimitCount: limit,
	})
	if err != nil {
		s.writeInternalError(w, err)
		return
	}

	items := make([]apitypes.InboxItem, 0, len(deliverables)+len(schedulerRuns))
	for _, row := range deliverables {
		items = append(items, deliverableInboxItem(row))
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

func nullableStringParam(value *string) any {
	if value == nil || *value == "" {
		return nil
	}
	return *value
}

// deliverableInboxItem renders a blocked or terminally-failed deliverable as an
// inbox entry. The kind and detail are derived from lifecycle/block_reason — the
// deliverable model has no per-block message, so the reason IS the detail.
func deliverableInboxItem(row sqlc.ListInboxDeliverablesRow) apitypes.InboxItem {
	kind, prefix, detail := inboxFacetForDeliverable(row.Lifecycle, row.BlockReason)
	return apitypes.InboxItem{
		Id:         prefix + ":" + row.ID,
		Kind:       kind,
		Title:      row.Title,
		Detail:     &detail,
		AgentId:    &row.AgentID,
		ProjectId:  nullableStringPtr(row.ProjectID),
		SourceType: apitypes.InboxItemSourceTypeDeliverable,
		SourceId:   row.ID,
		TargetPath: deliverableTargetPath(row.AgentID, row.ID),
		CreatedAt:  parseTime(row.UpdatedAt),
	}
}

// inboxFacetForDeliverable maps a deliverable's lifecycle/block_reason to its
// inbox kind, id prefix, and a human-readable detail line.
func inboxFacetForDeliverable(lifecycle, blockReason string) (apitypes.InboxItemKind, string, string) {
	switch {
	case lifecycle == deliverable.LifecycleBlocked && blockReason == deliverable.BlockNeedsVerdict:
		return apitypes.InboxItemKindReview, "review", "Awaiting your verdict"
	case lifecycle == deliverable.LifecycleBlocked && blockReason == deliverable.BlockBudgetExhausted:
		return apitypes.InboxItemKindBlocked, "blocked", "Attempt budget exhausted"
	case lifecycle == deliverable.LifecycleBlocked:
		return apitypes.InboxItemKindBlocked, "blocked", "Blocked on a dependency"
	case lifecycle == deliverable.LifecycleAbandoned:
		return apitypes.InboxItemKindFailed, "failed", "Abandoned after budget exhaustion"
	default: // rejected_final
		return apitypes.InboxItemKindFailed, "failed", "Rejected with no rework path left"
	}
}

func failedSchedulerRunInboxItem(row sqlc.ListFailedInboxSchedulerRunsRow) apitypes.InboxItem {
	createdAt := parseTimePtr(row.FinishedAt)
	if createdAt == nil {
		t := parseTime(row.StartedAt)
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

func deliverableTargetPath(agentID, deliverableID string) string {
	return "/agents/" + agentID + "/deliverables/" + deliverableID
}

func schedulerRunTargetPath(row sqlc.ListFailedInboxSchedulerRunsRow) string {
	if row.AgentID.Valid {
		return "/agents/" + row.AgentID.String + "/tasks"
	}
	return "/agents"
}
