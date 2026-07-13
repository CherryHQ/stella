package memorywrite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

type KnowledgeState string

const (
	KnowledgeStateActive  KnowledgeState = "active"
	KnowledgeStateRemoved KnowledgeState = "removed"
)

type KnowledgeRemovalSource string

const (
	KnowledgeRemovalManual  KnowledgeRemovalSource = "manual"
	KnowledgeRemovalCurator KnowledgeRemovalSource = "curator"
)

const KnowledgeRestoreWindow = 90 * 24 * time.Hour

type KnowledgeItem struct {
	Fact            memory.Fact
	RemovalSource   KnowledgeRemovalSource
	DeprecatedAt    *time.Time
	RestoreDeadline *time.Time
	IsRestorable    bool
}

// KnowledgeCursor identifies the last visible row in a stable lifecycle page.
type KnowledgeCursor struct {
	Timestamp time.Time
	ID        string
}

type KnowledgeListQuery struct {
	UserID  string
	AgentID string
	State   KnowledgeState
	Limit   int32
	Now     time.Time
	Cursor  *KnowledgeCursor
}

type KnowledgePage struct {
	Items      []KnowledgeItem
	Total      int64
	HasMore    bool
	NextCursor *KnowledgeCursor
}

type KnowledgeCreateInput struct{ UserID, AgentID, Content string }

type KnowledgeReplaceInput struct{ FactID, UserID, AgentID, Content string }

type KnowledgeDeprecateInput struct{ FactID, UserID, AgentID, DeprecatedBy string }

type KnowledgeRestoreInput struct {
	FactID, UserID, AgentID, RestoredBy string
	Now                                 time.Time
}

type KnowledgeRestoreResult struct {
	Fact     memory.Fact
	Restored bool
}

// ListKnowledge returns one deterministic active or eligible removed knowledge page.
func ListKnowledge(ctx context.Context, q *sqlc.Queries, in KnowledgeListQuery) (KnowledgePage, error) {
	if q == nil {
		return KnowledgePage{}, fmt.Errorf("list knowledge: sql queries are required")
	}
	if in.Limit < 0 {
		return KnowledgePage{}, fmt.Errorf("list knowledge: limit must be non-negative")
	}
	if err := validateKnowledgeCursor(in.Cursor); err != nil {
		return KnowledgePage{}, err
	}
	if in.Now.IsZero() {
		in.Now = time.Now().UTC()
	} else {
		in.Now = in.Now.UTC()
	}
	limitPlusOne := in.Limit + 1
	cursorTimestamp, cursorID := knowledgeCursorParams(in.Cursor)

	switch in.State {
	case KnowledgeStateActive:
		total, err := q.CountActiveKnowledge(ctx, sqlc.CountActiveKnowledgeParams{UserID: in.UserID, AgentID: in.AgentID})
		if err != nil {
			return KnowledgePage{}, fmt.Errorf("count active knowledge: %w", err)
		}
		rows, err := q.ListActiveKnowledge(ctx, sqlc.ListActiveKnowledgeParams{
			UserID: in.UserID, AgentID: in.AgentID, CursorTimestamp: cursorTimestamp, CursorID: cursorID, LimitCount: limitPlusOne,
		})
		if err != nil {
			return KnowledgePage{}, fmt.Errorf("list active knowledge: %w", err)
		}
		items := make([]KnowledgeItem, 0, len(rows))
		for _, row := range rows {
			items = append(items, KnowledgeItem{Fact: factFromRow(row)})
		}
		return knowledgePage(items, total, in.Limit), nil
	case KnowledgeStateRemoved:
		total, err := q.CountRemovedKnowledge(ctx, sqlc.CountRemovedKnowledgeParams{
			UserID: in.UserID, AgentID: in.AgentID, NowAt: in.Now,
		})
		if err != nil {
			return KnowledgePage{}, fmt.Errorf("count removed knowledge: %w", err)
		}
		rows, err := q.ListRemovedKnowledge(ctx, sqlc.ListRemovedKnowledgeParams{
			UserID: in.UserID, AgentID: in.AgentID, NowAt: in.Now, CursorTimestamp: cursorTimestamp, CursorID: cursorID, LimitCount: limitPlusOne,
		})
		if err != nil {
			return KnowledgePage{}, fmt.Errorf("list removed knowledge: %w", err)
		}
		items := make([]KnowledgeItem, 0, len(rows))
		for _, row := range rows {
			item, err := knowledgeItemFromRemovedRow(row)
			if err != nil {
				return KnowledgePage{}, err
			}
			items = append(items, item)
		}
		return knowledgePage(items, total, in.Limit), nil
	default:
		return KnowledgePage{}, fmt.Errorf("list knowledge: unsupported state %q", in.State)
	}
}

// CreateKnowledge creates a trimmed, manually owned world fact.
func CreateKnowledge(ctx context.Context, db *pgxpool.Pool, q *sqlc.Queries, in KnowledgeCreateInput) (memory.Fact, error) {
	content, err := requiredKnowledgeContent(in.Content)
	if err != nil {
		return memory.Fact{}, err
	}
	tx, err := beginKnowledgeMutation(ctx, db, q, in.UserID, in.AgentID)
	if err != nil {
		return memory.Fact{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	fact, err := applyFactWriteLocked(ctx, q.WithTx(tx), factWritePlan{
		action: "create",
		write: memory.FactWrite{
			UserID: in.UserID, AgentID: in.AgentID, Subject: memory.FactSubjectWorld, Content: content, Source: memory.SourceManual,
		},
	})
	if err != nil {
		return memory.Fact{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return memory.Fact{}, fmt.Errorf("commit knowledge create: %w", err)
	}
	return fact, nil
}

// ReplaceKnowledge atomically retires one active world fact and writes its manual successor.
func ReplaceKnowledge(ctx context.Context, db *pgxpool.Pool, q *sqlc.Queries, in KnowledgeReplaceInput) (memory.Fact, error) {
	content, err := requiredKnowledgeContent(in.Content)
	if err != nil {
		return memory.Fact{}, err
	}
	tx, err := beginKnowledgeMutation(ctx, db, q, in.UserID, in.AgentID)
	if err != nil {
		return memory.Fact{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := q.WithTx(tx)
	if _, err := requireActiveKnowledgeForUpdate(ctx, qtx, in.FactID, in.UserID, in.AgentID); err != nil {
		return memory.Fact{}, err
	}

	fact, err := applyFactWriteLocked(ctx, qtx, factWritePlan{
		action: "replace", oldFactID: in.FactID,
		write: memory.FactWrite{
			UserID: in.UserID, AgentID: in.AgentID, Subject: memory.FactSubjectWorld, Content: content, Supersedes: in.FactID, Source: memory.SourceManual,
		},
	})
	if err != nil {
		return memory.Fact{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return memory.Fact{}, fmt.Errorf("commit knowledge replace: %w", err)
	}
	return fact, nil
}

// DeprecateKnowledge manually retires an active world fact and records its operator.
func DeprecateKnowledge(ctx context.Context, db *pgxpool.Pool, q *sqlc.Queries, in KnowledgeDeprecateInput) (memory.Fact, error) {
	if in.DeprecatedBy == "" {
		return memory.Fact{}, fmt.Errorf("deprecate knowledge: deprecated_by is required")
	}
	tx, err := beginKnowledgeMutation(ctx, db, q, in.UserID, in.AgentID)
	if err != nil {
		return memory.Fact{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := q.WithTx(tx)
	before, err := requireActiveKnowledgeForUpdate(ctx, qtx, in.FactID, in.UserID, in.AgentID)
	if err != nil {
		return memory.Fact{}, err
	}
	beforeVersion, err := currentMemoryVersion(ctx, qtx, in.UserID, in.AgentID)
	if err != nil {
		return memory.Fact{}, err
	}
	row, err := qtx.DeprecateFact(ctx, sqlc.DeprecateFactParams{ID: in.FactID, UserID: in.UserID, AgentID: in.AgentID})
	if err != nil {
		return memory.Fact{}, fmt.Errorf("deprecate knowledge fact: %w", err)
	}
	deprecated := factFromRow(row)
	if err := deleteKnowledgeUsageIfReflectWorld(ctx, qtx, before); err != nil {
		return memory.Fact{}, err
	}
	memoryRow, err := qtx.BumpAgentMemoryVersion(ctx, sqlc.BumpAgentMemoryVersionParams{UserID: in.UserID, AgentID: in.AgentID})
	if err != nil {
		return memory.Fact{}, fmt.Errorf("bump memory version: %w", err)
	}
	metadata, err := json.Marshal(map[string]string{
		"deprecated_by":         "manual",
		"deprecated_by_user_id": in.DeprecatedBy,
	})
	if err != nil {
		return memory.Fact{}, fmt.Errorf("marshal manual deprecation metadata: %w", err)
	}
	if _, err := writeFactChangelogWithMetadata(ctx, qtx, in.UserID, in.AgentID, "deprecate", memory.SourceManual, beforeVersion, memoryRow.Version, before, deprecated, metadata); err != nil {
		return memory.Fact{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return memory.Fact{}, fmt.Errorf("commit knowledge deprecate: %w", err)
	}
	return deprecated, nil
}

// RestoreKnowledge restores an eligible removed world fact within its 90-day window.
func RestoreKnowledge(ctx context.Context, db *pgxpool.Pool, q *sqlc.Queries, in KnowledgeRestoreInput) (KnowledgeRestoreResult, error) {
	if in.RestoredBy == "" {
		return KnowledgeRestoreResult{}, ErrFactRestoreBadCaller
	}
	if in.Now.IsZero() {
		in.Now = time.Now().UTC()
	} else {
		in.Now = in.Now.UTC()
	}
	tx, err := beginKnowledgeMutation(ctx, db, q, in.UserID, in.AgentID)
	if err != nil {
		return KnowledgeRestoreResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := q.WithTx(tx)

	row, err := qtx.GetFactForUpdate(ctx, sqlc.GetFactForUpdateParams{ID: in.FactID, UserID: in.UserID, AgentID: in.AgentID})
	if errors.Is(err, pgx.ErrNoRows) {
		return KnowledgeRestoreResult{}, ErrFactNotRestorable
	}
	if err != nil {
		return KnowledgeRestoreResult{}, fmt.Errorf("lock knowledge for restore: %w", err)
	}
	before := factFromRow(row)
	if before.Scope != "user_agent" || before.Subject != memory.FactSubjectWorld {
		return KnowledgeRestoreResult{}, ErrFactNotRestorable
	}
	if before.Status == memory.FactStatusActive {
		if err := tx.Commit(ctx); err != nil {
			return KnowledgeRestoreResult{}, fmt.Errorf("commit no-op knowledge restore: %w", err)
		}
		return KnowledgeRestoreResult{Fact: before}, nil
	}
	if before.Status != memory.FactStatusDeprecated {
		return KnowledgeRestoreResult{}, ErrFactNotRestorable
	}

	deprecateLog, err := qtx.GetLatestQualifyingKnowledgeDeprecateChangelog(ctx, sqlc.GetLatestQualifyingKnowledgeDeprecateChangelogParams{
		FactID: in.FactID, UserID: in.UserID, AgentID: in.AgentID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return KnowledgeRestoreResult{}, ErrFactNotRestorable
	}
	if err != nil {
		return KnowledgeRestoreResult{}, fmt.Errorf("read knowledge deprecate changelog: %w", err)
	}
	if !in.Now.Before(deprecateLog.CreatedAt.Add(KnowledgeRestoreWindow)) {
		return KnowledgeRestoreResult{}, ErrFactRestoreExpired
	}
	contents, err := qtx.ListActiveKnowledgeContents(ctx, sqlc.ListActiveKnowledgeContentsParams{
		UserID: in.UserID, AgentID: in.AgentID,
	})
	if err != nil {
		return KnowledgeRestoreResult{}, fmt.Errorf("list active knowledge contents: %w", err)
	}
	for _, content := range contents {
		if strings.TrimSpace(content) == strings.TrimSpace(before.Content) {
			return KnowledgeRestoreResult{}, ErrFactDuplicateContent
		}
	}

	beforeVersion, err := currentMemoryVersion(ctx, qtx, in.UserID, in.AgentID)
	if err != nil {
		return KnowledgeRestoreResult{}, err
	}
	restoredRow, err := qtx.RestoreKnowledgeFact(ctx, sqlc.RestoreKnowledgeFactParams{ID: in.FactID, UserID: in.UserID, AgentID: in.AgentID})
	if errors.Is(err, pgx.ErrNoRows) {
		return KnowledgeRestoreResult{}, ErrFactNotRestorable
	}
	if err != nil {
		return KnowledgeRestoreResult{}, fmt.Errorf("restore knowledge fact: %w", err)
	}
	restored := factFromRow(restoredRow)
	memoryRow, err := qtx.BumpAgentMemoryVersion(ctx, sqlc.BumpAgentMemoryVersionParams{UserID: in.UserID, AgentID: in.AgentID})
	if err != nil {
		return KnowledgeRestoreResult{}, fmt.Errorf("bump memory version: %w", err)
	}
	if err := upsertKnowledgeUsageIfReflectWorld(ctx, qtx, restored); err != nil {
		return KnowledgeRestoreResult{}, err
	}
	if _, err := writeFactChangelogWithMetadata(ctx, qtx, in.UserID, in.AgentID, "restore", memory.SourceManual, beforeVersion, memoryRow.Version, before, restored, restoreFactMetadata(in.RestoredBy, "", deprecateLog)); err != nil {
		return KnowledgeRestoreResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return KnowledgeRestoreResult{}, fmt.Errorf("commit knowledge restore: %w", err)
	}
	return KnowledgeRestoreResult{Fact: restored, Restored: true}, nil
}

func beginKnowledgeMutation(ctx context.Context, db *pgxpool.Pool, q *sqlc.Queries, userID string, agentID string) (pgx.Tx, error) {
	if db == nil || q == nil {
		return nil, fmt.Errorf("knowledge mutation: db and sql queries are required")
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin knowledge mutation: %w", err)
	}
	if err := lockMemory(ctx, tx, userID, agentID); err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}

func requiredKnowledgeContent(content string) (string, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return "", fmt.Errorf("knowledge content is required")
	}
	return content, nil
}

// requireActiveKnowledgeForUpdate keeps replacement and deletion decisions serialized.
func requireActiveKnowledgeForUpdate(ctx context.Context, q *sqlc.Queries, factID string, userID string, agentID string) (memory.Fact, error) {
	row, err := q.GetFactForUpdate(ctx, sqlc.GetFactForUpdateParams{ID: factID, UserID: userID, AgentID: agentID})
	if errors.Is(err, pgx.ErrNoRows) {
		return memory.Fact{}, ErrFactNotRestorable
	}
	if err != nil {
		return memory.Fact{}, fmt.Errorf("lock active knowledge: %w", err)
	}
	fact := factFromRow(row)
	if fact.Scope != "user_agent" || fact.Subject != memory.FactSubjectWorld || fact.Status != memory.FactStatusActive {
		return memory.Fact{}, ErrFactNotRestorable
	}
	return fact, nil
}

func knowledgePage(items []KnowledgeItem, total int64, limit int32) KnowledgePage {
	page := KnowledgePage{Items: items, Total: total}
	if int32(len(page.Items)) > limit {
		page.Items = page.Items[:limit]
		page.HasMore = true
	}
	if len(page.Items) > 0 {
		last := page.Items[len(page.Items)-1]
		sortTimestamp := last.Fact.UpdatedAt
		if last.DeprecatedAt != nil {
			sortTimestamp = *last.DeprecatedAt
		}
		page.NextCursor = &KnowledgeCursor{Timestamp: sortTimestamp, ID: last.Fact.ID}
	}
	return page
}

func validateKnowledgeCursor(cursor *KnowledgeCursor) error {
	if cursor != nil && (cursor.Timestamp.IsZero() || cursor.ID == "") {
		return fmt.Errorf("list knowledge: cursor timestamp and id must be provided together")
	}
	return nil
}

func knowledgeCursorParams(cursor *KnowledgeCursor) (pgtype.Timestamptz, pgtype.Text) {
	if cursor == nil {
		return pgtype.Timestamptz{}, pgtype.Text{}
	}
	return pgtype.Timestamptz{Time: cursor.Timestamp.UTC(), Valid: true}, pgtype.Text{String: cursor.ID, Valid: true}
}

func knowledgeItemFromRemovedRow(row sqlc.ListRemovedKnowledgeRow) (KnowledgeItem, error) {
	removalSource, err := knowledgeRemovalSource(row.DeprecateMetadata.String)
	if err != nil {
		return KnowledgeItem{}, err
	}
	deprecatedAt := row.DeprecatedAt.UTC()
	deadline := deprecatedAt.Add(KnowledgeRestoreWindow)
	supersedes := ""
	if row.Supersedes.Valid {
		supersedes = row.Supersedes.String
	}
	return KnowledgeItem{
		Fact: memory.Fact{
			ID: row.ID, Subject: memory.FactSubject(row.Subject), Scope: row.Scope, UserID: row.UserID, AgentID: row.AgentID,
			Content: row.Content, Status: memory.FactStatus(row.Status), Metadata: row.Metadata, Version: row.Version,
			Source: memory.ChangeSource(row.Source), CreatedAt: row.CreatedAt.UTC(), UpdatedAt: row.UpdatedAt.UTC(),
			Supersedes: supersedes,
		},
		RemovalSource: removalSource, DeprecatedAt: &deprecatedAt, RestoreDeadline: &deadline, IsRestorable: true,
	}, nil
}

func knowledgeRemovalSource(metadata string) (KnowledgeRemovalSource, error) {
	var values map[string]string
	if err := json.Unmarshal([]byte(metadata), &values); err != nil {
		return "", fmt.Errorf("parse knowledge deprecate metadata: %w", err)
	}
	if values["deprecated_by"] == "manual" {
		return KnowledgeRemovalManual, nil
	}
	if values["curator"] == "usage" {
		return KnowledgeRemovalCurator, nil
	}
	return "", ErrFactNotRestorable
}
