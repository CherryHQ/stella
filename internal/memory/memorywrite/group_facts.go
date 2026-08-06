package memorywrite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/db/pgnull"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

const maxGroupFactTargetsPerOperation = 10

type ApplyGroupFactPlanRequest struct {
	GroupID   string
	Pipeline  string
	Watermark int64
	Plan      memory.GroupFactPlan
}

type ApplyGroupFactPlanResult struct {
	Version           int64
	ChangedOperations int
	CreatedFactIDs    []string
	CheckpointNoop    bool
}

// ApplyGroupFactPlan commits one reviewed Group Reflect window. Facts,
// changelog, version, and cursor share one short transaction under a per-group
// advisory lock.
func ApplyGroupFactPlan(
	ctx context.Context,
	db *pgxpool.Pool,
	q *sqlc.Queries,
	req ApplyGroupFactPlanRequest,
) (ApplyGroupFactPlanResult, error) {
	if db == nil || q == nil {
		return ApplyGroupFactPlanResult{}, fmt.Errorf("group fact writer requires db and queries")
	}
	normalized, err := normalizeGroupFactPlan(req)
	if err != nil {
		return ApplyGroupFactPlanResult{}, err
	}

	tx, err := db.Begin(ctx)
	if err != nil {
		return ApplyGroupFactPlanResult{}, fmt.Errorf("begin group fact transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := appdb.AdvisoryXactLock(ctx, tx, "group-memory:"+normalized.GroupID); err != nil {
		return ApplyGroupFactPlanResult{}, err
	}
	qtx := q.WithTx(tx)

	if _, err := qtx.EnsureGroupMemoryVersion(ctx, normalized.GroupID); err != nil {
		return ApplyGroupFactPlanResult{}, fmt.Errorf("ensure group memory version: %w", err)
	}
	version, err := qtx.GetGroupMemoryVersionForUpdate(ctx, normalized.GroupID)
	if err != nil {
		return ApplyGroupFactPlanResult{}, fmt.Errorf("lock group memory version: %w", err)
	}

	// A committed cursor proves the whole earlier window already committed,
	// because both values are written in this transaction.
	cursor, cursorErr := qtx.GetIngestCursor(ctx, sqlc.GetIngestCursorParams{
		GroupID:  normalized.GroupID,
		Pipeline: normalized.Pipeline,
	})
	if cursorErr == nil && cursor.LastSeq >= normalized.Watermark {
		if err := tx.Commit(ctx); err != nil {
			return ApplyGroupFactPlanResult{}, fmt.Errorf("commit idempotent group fact checkpoint: %w", err)
		}
		return ApplyGroupFactPlanResult{Version: version, CheckpointNoop: true}, nil
	}
	if cursorErr != nil && !errors.Is(cursorErr, pgx.ErrNoRows) {
		return ApplyGroupFactPlanResult{}, fmt.Errorf("read group fact checkpoint: %w", cursorErr)
	}

	result := ApplyGroupFactPlanResult{Version: version}
	for _, op := range normalized.Plan.Operations {
		if op.Action == memory.GroupFactActionNoop {
			continue
		}
		beforeVersion := result.Version
		afterVersion := beforeVersion + 1

		var targets []sqlc.CtxGroupFact
		if len(op.TargetFactIDs) > 0 {
			targets, err = qtx.ListGroupFactsByIDsForUpdate(ctx, sqlc.ListGroupFactsByIDsForUpdateParams{
				GroupID: normalized.GroupID,
				FactIds: op.TargetFactIDs,
			})
			if err != nil {
				return ApplyGroupFactPlanResult{}, fmt.Errorf("lock group fact targets: %w", err)
			}
			if err := validateLockedGroupFactTargets(op, targets); err != nil {
				return ApplyGroupFactPlanResult{}, err
			}
		}

		for _, target := range targets {
			after, deprecateErr := qtx.DeprecateGroupFact(ctx, sqlc.DeprecateGroupFactParams{
				GroupID: normalized.GroupID,
				ID:      target.ID,
			})
			if deprecateErr != nil {
				return ApplyGroupFactPlanResult{}, fmt.Errorf("deprecate group fact %s: %w", target.ID, deprecateErr)
			}
			action := "deprecate"
			if op.Action == memory.GroupFactActionReplaceMany {
				action = "replace"
			}
			if err := insertGroupFactChangelog(ctx, qtx, action, beforeVersion, afterVersion, target, &after); err != nil {
				return ApplyGroupFactPlanResult{}, err
			}
		}

		if op.Action == memory.GroupFactActionCreate || op.Action == memory.GroupFactActionReplaceMany {
			created, createErr := qtx.InsertGroupFact(ctx, sqlc.InsertGroupFactParams{
				ID:        uuid.Must(uuid.NewV7()).String(),
				GroupID:   normalized.GroupID,
				Subject:   string(op.Subject),
				SubjectID: pgnull.Text(op.SubjectID),
				Content:   op.NewContent,
				Status:    string(memory.GroupFactStatusActive),
				Source:    memory.GroupFactSourceReflect,
			})
			if createErr != nil {
				return ApplyGroupFactPlanResult{}, fmt.Errorf("insert group fact: %w", createErr)
			}
			action := "create"
			if op.Action == memory.GroupFactActionReplaceMany {
				action = "replace"
			}
			if err := insertGroupFactChangelog(ctx, qtx, action, beforeVersion, afterVersion, sqlc.CtxGroupFact{}, &created); err != nil {
				return ApplyGroupFactPlanResult{}, err
			}
			result.CreatedFactIDs = append(result.CreatedFactIDs, created.ID)
		}

		if err := qtx.UpdateGroupMemoryVersion(ctx, sqlc.UpdateGroupMemoryVersionParams{
			GroupID: normalized.GroupID,
			Version: afterVersion,
		}); err != nil {
			return ApplyGroupFactPlanResult{}, fmt.Errorf("update group fact version: %w", err)
		}
		result.Version = afterVersion
		result.ChangedOperations++
	}

	if err := qtx.UpsertIngestCursor(ctx, sqlc.UpsertIngestCursorParams{
		GroupID:  normalized.GroupID,
		Pipeline: normalized.Pipeline,
		LastSeq:  normalized.Watermark,
	}); err != nil {
		return ApplyGroupFactPlanResult{}, fmt.Errorf("advance group fact checkpoint: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return ApplyGroupFactPlanResult{}, fmt.Errorf("commit group fact transaction: %w", err)
	}
	result.CheckpointNoop = result.ChangedOperations == 0
	return result, nil
}

func normalizeGroupFactPlan(req ApplyGroupFactPlanRequest) (ApplyGroupFactPlanRequest, error) {
	// Requests may be reused by concurrent idempotent retries. Copy nested
	// slices before normalization so validation never mutates caller-owned data.
	operations := make([]memory.GroupFactOperation, len(req.Plan.Operations))
	for i, operation := range req.Plan.Operations {
		operation.TargetFactIDs = append([]string(nil), operation.TargetFactIDs...)
		operations[i] = operation
	}
	req.Plan.Operations = operations

	req.GroupID = strings.TrimSpace(req.GroupID)
	req.Pipeline = strings.TrimSpace(req.Pipeline)
	if req.GroupID == "" || req.Pipeline == "" {
		return ApplyGroupFactPlanRequest{}, fmt.Errorf("group_id and pipeline are required")
	}
	if req.Watermark < 0 {
		return ApplyGroupFactPlanRequest{}, fmt.Errorf("watermark must be non-negative")
	}

	seenTargets := make(map[string]struct{})
	for i := range req.Plan.Operations {
		op := &req.Plan.Operations[i]
		op.SubjectID = strings.TrimSpace(op.SubjectID)
		op.NewContent = strings.TrimSpace(op.NewContent)
		if err := validateGroupFactSubject(op.Subject, op.SubjectID); err != nil {
			return ApplyGroupFactPlanRequest{}, fmt.Errorf("operation %d: %w", i, err)
		}
		if len(op.TargetFactIDs) > maxGroupFactTargetsPerOperation {
			return ApplyGroupFactPlanRequest{}, fmt.Errorf("operation %d: at most %d targets are allowed", i, maxGroupFactTargetsPerOperation)
		}
		for targetIndex, targetID := range op.TargetFactIDs {
			targetID = strings.TrimSpace(targetID)
			if targetID == "" {
				return ApplyGroupFactPlanRequest{}, fmt.Errorf("operation %d: target id is required", i)
			}
			if _, exists := seenTargets[targetID]; exists {
				return ApplyGroupFactPlanRequest{}, fmt.Errorf("operation %d: target %s appears more than once", i, targetID)
			}
			seenTargets[targetID] = struct{}{}
			op.TargetFactIDs[targetIndex] = targetID
		}
		switch op.Action {
		case memory.GroupFactActionNoop:
			if len(op.TargetFactIDs) != 0 || op.NewContent != "" {
				return ApplyGroupFactPlanRequest{}, fmt.Errorf("operation %d: noop cannot mutate facts", i)
			}
		case memory.GroupFactActionCreate:
			if len(op.TargetFactIDs) != 0 || op.NewContent == "" {
				return ApplyGroupFactPlanRequest{}, fmt.Errorf("operation %d: create requires new content and no targets", i)
			}
		case memory.GroupFactActionReplaceMany:
			if len(op.TargetFactIDs) == 0 || op.NewContent == "" {
				return ApplyGroupFactPlanRequest{}, fmt.Errorf("operation %d: replace_many requires targets and new content", i)
			}
		case memory.GroupFactActionDeprecateMany:
			if len(op.TargetFactIDs) == 0 || op.NewContent != "" {
				return ApplyGroupFactPlanRequest{}, fmt.Errorf("operation %d: deprecate_many requires targets and no new content", i)
			}
		default:
			return ApplyGroupFactPlanRequest{}, fmt.Errorf("operation %d: unsupported action %q", i, op.Action)
		}
	}
	return req, nil
}

func validateGroupFactSubject(subject memory.GroupFactSubject, subjectID string) error {
	switch subject {
	case memory.GroupFactSubjectGroup:
		if subjectID != "" {
			return fmt.Errorf("group subject cannot have subject_id")
		}
	case memory.GroupFactSubjectHuman, memory.GroupFactSubjectAgent:
		if subjectID == "" {
			return fmt.Errorf("%s subject requires subject_id", subject)
		}
	default:
		return fmt.Errorf("unsupported group fact subject %q", subject)
	}
	return nil
}

func validateLockedGroupFactTargets(op memory.GroupFactOperation, rows []sqlc.CtxGroupFact) error {
	if len(rows) != len(op.TargetFactIDs) {
		return fmt.Errorf("group fact targets changed or crossed group scope")
	}
	for _, row := range rows {
		if row.Status != string(memory.GroupFactStatusActive) {
			return fmt.Errorf("group fact target %s is no longer active", row.ID)
		}
		if row.Subject != string(op.Subject) || row.SubjectID.String != op.SubjectID {
			return fmt.Errorf("group fact target %s changed typed subject", row.ID)
		}
	}
	return nil
}

func insertGroupFactChangelog(
	ctx context.Context,
	q *sqlc.Queries,
	action string,
	beforeVersion int64,
	afterVersion int64,
	before sqlc.CtxGroupFact,
	after *sqlc.CtxGroupFact,
) error {
	var beforeJSON []byte
	if before.ID != "" {
		var err error
		beforeJSON, err = json.Marshal(before)
		if err != nil {
			return fmt.Errorf("marshal group fact before state: %w", err)
		}
	}
	var afterJSON []byte
	factID := before.ID
	groupID := before.GroupID
	if after != nil {
		var err error
		afterJSON, err = json.Marshal(after)
		if err != nil {
			return fmt.Errorf("marshal group fact after state: %w", err)
		}
		factID = after.ID
		groupID = after.GroupID
	}
	if factID == "" || groupID == "" {
		return fmt.Errorf("group fact changelog requires fact and group ids")
	}
	if err := q.InsertGroupFactChangelog(ctx, sqlc.InsertGroupFactChangelogParams{
		ID:                 uuid.Must(uuid.NewV7()).String(),
		GroupID:            groupID,
		FactID:             factID,
		Action:             action,
		Source:             memory.GroupFactSourceReflect,
		GroupVersionBefore: beforeVersion,
		GroupVersionAfter:  afterVersion,
		BeforeState:        beforeJSON,
		AfterState:         afterJSON,
	}); err != nil {
		return fmt.Errorf("insert group fact changelog: %w", err)
	}
	return nil
}
