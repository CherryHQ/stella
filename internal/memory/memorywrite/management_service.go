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

var ErrFactNotFound = errors.New("fact not found")

// ManagementService is the narrow application port for the user-facing memory
// management surface. It owns persistence and logical-history projection so
// HTTP handlers never depend on raw SQL queries.
type ManagementService struct {
	db        *pgxpool.Pool
	q         *sqlc.Queries
	changelog memory.ChangelogPageReader
}

func NewManagementService(db *pgxpool.Pool, changelog memory.ChangelogPageReader) *ManagementService {
	return &ManagementService{db: db, q: sqlc.New(db), changelog: changelog}
}

func (s *ManagementService) ListKnowledge(ctx context.Context, in KnowledgeListQuery) (KnowledgePage, error) {
	return ListKnowledge(ctx, s.q, in)
}

func (s *ManagementService) CreateKnowledge(ctx context.Context, in KnowledgeCreateInput) (memory.Fact, error) {
	return CreateKnowledge(ctx, s.db, s.q, in)
}

func (s *ManagementService) ReplaceKnowledge(ctx context.Context, in KnowledgeReplaceInput) (memory.Fact, error) {
	return ReplaceKnowledge(ctx, s.db, s.q, in)
}

func (s *ManagementService) DeprecateKnowledge(ctx context.Context, in KnowledgeDeprecateInput) (memory.Fact, error) {
	return DeprecateKnowledge(ctx, s.db, s.q, in)
}

// RestoreKnowledge checks the authorized owner tuple before applying the
// lifecycle transition, keeping cross-owner existence hidden from transports.
func (s *ManagementService) RestoreKnowledge(ctx context.Context, in KnowledgeRestoreInput) (KnowledgeRestoreResult, error) {
	if _, err := s.q.GetFact(ctx, sqlc.GetFactParams{ID: in.FactID, UserID: in.UserID, AgentID: in.AgentID}); errors.Is(err, pgx.ErrNoRows) {
		return KnowledgeRestoreResult{}, ErrFactNotFound
	} else if err != nil {
		return KnowledgeRestoreResult{}, fmt.Errorf("read knowledge for restore: %w", err)
	}
	return RestoreKnowledge(ctx, s.db, s.q, in)
}

// ReadChangelogPage returns one logical page source. Profile and Soul remain
// provider-owned projections; Knowledge and Constraints are projected here.
func (s *ManagementService) ReadChangelogPage(
	ctx context.Context,
	userID string,
	agentID string,
	scope string,
	cursor *memory.ChangelogCursor,
	limit int,
) ([]memory.ChangeEntry, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("list memory changelog: limit must be positive")
	}
	switch scope {
	case "profile", "soul":
		if s.changelog == nil {
			return nil, fmt.Errorf("memory changelog page reader not configured")
		}
		return s.changelog.ReadChangelogPage(ctx, userID, agentID, scope, cursor, limit)
	case "knowledge":
		return s.readKnowledgeChangelogPage(ctx, userID, agentID, cursor, limit)
	case "constraint":
		cursorCreatedAt, cursorID := managementChangelogCursorParams(cursor)
		rows, err := s.q.ListMemoryChangelogPage(ctx, sqlc.ListMemoryChangelogPageParams{
			UserID: userID, AgentID: agentID, Scope: scope,
			CursorCreatedAt: cursorCreatedAt, CursorID: cursorID, LimitCount: int32(limit),
		})
		if err != nil {
			return nil, fmt.Errorf("list constraint changelog page: %w", err)
		}
		entries := make([]memory.ChangeEntry, len(rows))
		for i, row := range rows {
			entries[i] = managementChangelogRow(row)
		}
		return entries, nil
	default:
		return nil, fmt.Errorf("unsupported changelog scope %q", scope)
	}
}

func (s *ManagementService) readKnowledgeChangelogPage(ctx context.Context, userID string, agentID string, cursor *memory.ChangelogCursor, limit int) ([]memory.ChangeEntry, error) {
	entries := make([]memory.ChangeEntry, 0, limit)
	batchCursor := cursor
	for len(entries) < limit {
		// Some low-level groups are history-only. Continue advancing the raw
		// keyset until the requested logical page is full or storage is exhausted.
		batchLimit := limit - len(entries)
		cursorCreatedAt, cursorID := managementChangelogCursorParams(batchCursor)
		rows, err := s.q.ListFactChangelogBySubjectPage(ctx, sqlc.ListFactChangelogBySubjectPageParams{
			UserID: userID, AgentID: agentID,
			Subject:         pgtype.Text{String: string(memory.FactSubjectWorld), Valid: true},
			CursorCreatedAt: cursorCreatedAt, CursorID: cursorID, LimitCount: int32(batchLimit),
		})
		if err != nil {
			return nil, fmt.Errorf("list knowledge changelog page: %w", err)
		}

		groups := groupManagementKnowledgeRows(rows)
		if len(groups) == 0 {
			break
		}
		for _, group := range groups {
			entry, ok, err := s.projectKnowledgeChangelogGroup(ctx, userID, agentID, group)
			if err != nil {
				return nil, err
			}
			if ok {
				entries = append(entries, entry)
			}
		}

		lastKey := groups[len(groups)-1][0]
		batchCursor = &memory.ChangelogCursor{CreatedAt: lastKey.CreatedAt, ID: lastKey.ID}
		if len(groups) < batchLimit {
			break
		}
	}
	return entries, nil
}

func groupManagementKnowledgeRows(rows []sqlc.ListFactChangelogBySubjectPageRow) [][]sqlc.CtxAgentMemoryChangelog {
	groups := make([][]sqlc.CtxAgentMemoryChangelog, 0, len(rows))
	var currentVersion int64
	for _, pageRow := range rows {
		row := sqlc.CtxAgentMemoryChangelog(pageRow)
		if len(groups) == 0 || row.MemoryVersionAfter.Int64 != currentVersion {
			currentVersion = row.MemoryVersionAfter.Int64
			groups = append(groups, nil)
		}
		groups[len(groups)-1] = append(groups[len(groups)-1], row)
	}
	return groups
}

type managementKnowledgeState struct {
	row    sqlc.CtxAgentMemoryChangelog
	before *memory.Fact
	after  *memory.Fact
}

func (s *ManagementService) projectKnowledgeChangelogGroup(ctx context.Context, userID string, agentID string, rows []sqlc.CtxAgentMemoryChangelog) (memory.ChangeEntry, bool, error) {
	var active *managementKnowledgeState
	var deprecated *managementKnowledgeState
	for _, row := range rows {
		before, err := parseManagementKnowledgeFact(row.BeforeText)
		if err != nil {
			return memory.ChangeEntry{}, false, err
		}
		after, err := parseManagementKnowledgeFact(row.AfterText)
		if err != nil {
			return memory.ChangeEntry{}, false, err
		}
		if after == nil || after.Subject != memory.FactSubjectWorld {
			continue
		}
		state := managementKnowledgeState{row: row, before: before, after: after}
		switch after.Status {
		case memory.FactStatusActive:
			active = &state
		case memory.FactStatusDeprecated:
			deprecated = &state
		}
	}

	if active != nil {
		entry := managementChangelogRow(active.row)
		entry.Scope = "knowledge"
		entry.AfterText = active.after.Content
		switch active.row.Action {
		case "replace":
			entry.Action = "edit"
			beforeText, found, err := s.replacedKnowledgeBeforeText(ctx, userID, agentID, active.after.Metadata)
			if err != nil {
				return memory.ChangeEntry{}, false, err
			}
			if found {
				entry.BeforeText = beforeText
			} else if deprecated != nil && deprecated.before != nil {
				entry.BeforeText = deprecated.before.Content
			}
		case "create":
			entry.Action = "create"
			entry.BeforeText = ""
		case "restore":
			entry.Action = "restore"
			entry.BeforeText = ""
		default:
			return memory.ChangeEntry{}, false, nil
		}
		return entry, true, nil
	}
	if deprecated == nil || deprecated.before == nil {
		return memory.ChangeEntry{}, false, nil
	}
	action, ok, err := managementKnowledgeDeprecationAction(deprecated.row.Metadata)
	if err != nil || !ok {
		return memory.ChangeEntry{}, false, err
	}
	entry := managementChangelogRow(deprecated.row)
	entry.Scope = "knowledge"
	entry.Action = action
	entry.BeforeText = deprecated.before.Content
	entry.AfterText = ""
	return entry, true, nil
}

func (s *ManagementService) replacedKnowledgeBeforeText(ctx context.Context, userID string, agentID string, metadata json.RawMessage) (string, bool, error) {
	if len(metadata) == 0 {
		return "", false, nil
	}
	var replacement struct {
		ReplacedFactIDs []string `json:"replaced_fact_ids"`
	}
	if err := json.Unmarshal(metadata, &replacement); err != nil {
		return "", false, fmt.Errorf("parse replacement knowledge metadata: %w", err)
	}
	if len(replacement.ReplacedFactIDs) == 0 {
		return "", false, nil
	}
	contents := make([]string, 0, len(replacement.ReplacedFactIDs))
	for _, factID := range replacement.ReplacedFactIDs {
		fact, err := s.q.GetFact(ctx, sqlc.GetFactParams{ID: factID, UserID: userID, AgentID: agentID})
		if err != nil {
			return "", false, fmt.Errorf("read replaced knowledge fact %q: %w", factID, err)
		}
		contents = append(contents, fact.Content)
	}
	return strings.Join(contents, "\n\n"), true, nil
}

func managementKnowledgeDeprecationAction(metadata pgtype.Text) (string, bool, error) {
	if !metadata.Valid || metadata.String == "" {
		return "", false, nil
	}
	var values map[string]any
	if err := json.Unmarshal([]byte(metadata.String), &values); err != nil {
		return "", false, fmt.Errorf("parse knowledge changelog metadata: %w", err)
	}
	if values["deprecated_by"] == "manual" {
		return "manual_delete", true, nil
	}
	if values["curator"] == "usage" {
		return "curator_remove", true, nil
	}
	return "", false, nil
}

func parseManagementKnowledgeFact(text pgtype.Text) (*memory.Fact, error) {
	if !text.Valid || text.String == "" {
		return nil, nil
	}
	var fact memory.Fact
	if err := json.Unmarshal([]byte(text.String), &fact); err != nil {
		return nil, fmt.Errorf("parse knowledge changelog fact: %w", err)
	}
	return &fact, nil
}

func managementChangelogCursorParams(cursor *memory.ChangelogCursor) (pgtype.Timestamptz, pgtype.Text) {
	if cursor == nil {
		return pgtype.Timestamptz{}, pgtype.Text{}
	}
	return pgtype.Timestamptz{Time: cursor.CreatedAt.UTC(), Valid: true}, pgtype.Text{String: cursor.ID, Valid: true}
}

func managementChangelogRow(row sqlc.CtxAgentMemoryChangelog) memory.ChangeEntry {
	entry := memory.ChangeEntry{
		ID: row.ID, UserID: row.UserID, AgentID: row.AgentID, Scope: row.Scope,
		Action: row.Action, Source: memory.ChangeSource(row.Source),
		CreatedAt: row.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
	if row.SessionID.Valid {
		entry.SessionID = row.SessionID.String
	}
	if row.MemoryVersionBefore.Valid {
		value := row.MemoryVersionBefore.Int64
		entry.MemoryVersionBefore = &value
	}
	if row.MemoryVersionAfter.Valid {
		value := row.MemoryVersionAfter.Int64
		entry.MemoryVersionAfter = &value
	}
	if row.BeforeText.Valid {
		entry.BeforeText = row.BeforeText.String
	}
	if row.AfterText.Valid {
		entry.AfterText = row.AfterText.String
	}
	return entry
}
