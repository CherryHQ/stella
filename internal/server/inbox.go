package server

import (
	"database/sql"
	"net/http"
	"sort"
	"time"

	apiserver "github.com/CherryHQ/stella/api/server"
	apitypes "github.com/CherryHQ/stella/api/types"
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

	blocked, err := s.q.ListBlockedInboxTasks(ctx, sqlc.ListBlockedInboxTasksParams{
		UserID:     info.UserID,
		AgentID:    agentID,
		LimitCount: limit,
	})
	if err != nil {
		s.writeInternalError(w, err)
		return
	}
	reviews, err := s.q.ListReviewInboxTasks(ctx, sqlc.ListReviewInboxTasksParams{
		UserID:     info.UserID,
		AgentID:    agentID,
		LimitCount: limit,
	})
	if err != nil {
		s.writeInternalError(w, err)
		return
	}
	taskRuns, err := s.q.ListFailedInboxTaskRuns(ctx, sqlc.ListFailedInboxTaskRunsParams{
		UserID:     info.UserID,
		Since:      sql.NullString{String: since, Valid: true},
		AgentID:    agentID,
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

	items := make([]apitypes.InboxItem, 0, len(blocked)+len(reviews)+len(taskRuns)+len(schedulerRuns))
	for _, row := range blocked {
		items = append(items, blockedInboxItem(row))
	}
	for _, row := range reviews {
		items = append(items, reviewInboxItem(row))
	}
	for _, row := range taskRuns {
		items = append(items, failedTaskRunInboxItem(row))
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

func blockedInboxItem(row sqlc.ListBlockedInboxTasksRow) apitypes.InboxItem {
	return apitypes.InboxItem{
		Id:         "blocked:" + row.TaskID,
		Kind:       apitypes.InboxItemKindBlocked,
		Title:      row.Title,
		Detail:     optionalString(row.Question),
		AgentId:    &row.AgentID,
		ProjectId:  nullableStringPtr(row.ProjectID),
		SourceType: apitypes.InboxItemSourceTypeTask,
		SourceId:   row.TaskID,
		TargetPath: taskTargetPath(row.AgentID, row.TaskID),
		CreatedAt:  parseTime(row.CreatedAt),
	}
}

func reviewInboxItem(row sqlc.ListReviewInboxTasksRow) apitypes.InboxItem {
	return apitypes.InboxItem{
		Id:         "review:" + row.ReviewID,
		Kind:       apitypes.InboxItemKindReview,
		Title:      row.Title,
		Detail:     optionalString(row.Summary),
		AgentId:    &row.AgentID,
		ProjectId:  nullableStringPtr(row.ProjectID),
		SourceType: apitypes.InboxItemSourceTypeTask,
		SourceId:   row.TaskID,
		TargetPath: taskTargetPath(row.AgentID, row.TaskID),
		CreatedAt:  parseTime(row.CreatedAt),
	}
}

func failedTaskRunInboxItem(row sqlc.ListFailedInboxTaskRunsRow) apitypes.InboxItem {
	createdAt := parseTimePtr(row.FinishedAt)
	if createdAt == nil {
		t := parseTime(row.CreatedAt)
		createdAt = &t
	}
	return apitypes.InboxItem{
		Id:         "task-run:" + row.RunID,
		Kind:       apitypes.InboxItemKindFailed,
		Title:      nullableStringValue(row.Title, "Task run failed"),
		Detail:     optionalString(row.Error),
		AgentId:    nullableStringPtr(row.AgentID),
		ProjectId:  nullableStringPtr(row.ProjectID),
		SourceType: apitypes.InboxItemSourceTypeTaskRun,
		SourceId:   row.RunID,
		TargetPath: taskRunTargetPath(row),
		CreatedAt:  *createdAt,
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

func nullableStringValue(ns sql.NullString, fallback string) string {
	if !ns.Valid || ns.String == "" {
		return fallback
	}
	return ns.String
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func taskTargetPath(agentID, taskID string) string {
	return "/agents/" + agentID + "/automations/tasks/" + taskID
}

func taskRunTargetPath(row sqlc.ListFailedInboxTaskRunsRow) string {
	if row.AgentID.Valid && row.TaskID.Valid {
		return taskTargetPath(row.AgentID.String, row.TaskID.String)
	}
	// Goal-owned runs have no task; land on the agent's work hub.
	if row.AgentID.Valid {
		return "/agents/" + row.AgentID.String + "/automations"
	}
	return "/agents"
}

func schedulerRunTargetPath(row sqlc.ListFailedInboxSchedulerRunsRow) string {
	if row.AgentID.Valid {
		return "/agents/" + row.AgentID.String + "/automations"
	}
	return "/agents"
}
