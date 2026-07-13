package server

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	apiserver "github.com/CherryHQ/stella/api/server"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func (s *Server) readProfileChangelogScope(
	ctx context.Context,
	userID string,
	agentID string,
	scope string,
	cursor *memory.ChangelogCursor,
	limit int,
) ([]apiserver.ChangelogEntry, error) {
	switch scope {
	case "profile", "soul":
		reader, ok := s.mem.(memory.ChangelogPageReader)
		if !ok {
			return nil, fmt.Errorf("memory changelog page reader not configured")
		}
		rows, err := reader.ReadChangelogPage(ctx, userID, agentID, scope, cursor, limit)
		if err != nil {
			return nil, err
		}
		entries := make([]apiserver.ChangelogEntry, len(rows))
		for i, row := range rows {
			entries[i] = memoryChangelogEntryToAPI(row)
		}
		return entries, nil
	case "knowledge":
		return s.readKnowledgeChangelogPage(ctx, userID, agentID, cursor, limit)
	case "constraint":
		cursorCreatedAt, cursorID := serverChangelogCursorParams(cursor)
		rows, err := s.q.ListMemoryChangelogPage(ctx, sqlc.ListMemoryChangelogPageParams{
			UserID: userID, AgentID: agentID, Scope: scope,
			CursorCreatedAt: cursorCreatedAt, CursorID: cursorID, LimitCount: int32(limit),
		})
		if err != nil {
			return nil, fmt.Errorf("list constraint changelog page: %w", err)
		}
		entries := make([]apiserver.ChangelogEntry, len(rows))
		for i, row := range rows {
			entries[i] = profileChangelogEntryToAPI(row)
		}
		return entries, nil
	default:
		return nil, fmt.Errorf("unsupported changelog scope %q", scope)
	}
}

func (s *Server) readKnowledgeChangelogPage(ctx context.Context, userID string, agentID string, cursor *memory.ChangelogCursor, limit int) ([]apiserver.ChangelogEntry, error) {
	entries := make([]apiserver.ChangelogEntry, 0, limit)
	batchCursor := cursor
	for len(entries) < limit {
		// A raw memory-version group may not project to a user-visible entry, so
		// keep advancing the keyset cursor until the logical page is full or empty.
		batchLimit := limit - len(entries)
		cursorCreatedAt, cursorID := serverChangelogCursorParams(batchCursor)
		pageRows, err := s.q.ListFactChangelogBySubjectPage(ctx, sqlc.ListFactChangelogBySubjectPageParams{
			UserID: userID, AgentID: agentID,
			Subject:         pgtype.Text{String: string(memory.FactSubjectWorld), Valid: true},
			CursorCreatedAt: cursorCreatedAt, CursorID: cursorID, LimitCount: int32(batchLimit),
		})
		if err != nil {
			return nil, fmt.Errorf("list knowledge changelog page: %w", err)
		}

		groups := groupKnowledgeChangelogRows(pageRows)
		if len(groups) == 0 {
			break
		}
		for _, rows := range groups {
			entry, ok, err := s.projectKnowledgeChangelogGroup(ctx, userID, agentID, rows)
			if err != nil {
				return nil, err
			}
			if ok {
				entries = append(entries, entry)
			}
		}

		// The first row is the group's SQL key because rows are ordered by the
		// selected group key and then by row timestamp/id descending.
		lastGroupKey := groups[len(groups)-1][0]
		batchCursor = &memory.ChangelogCursor{CreatedAt: lastGroupKey.CreatedAt, ID: lastGroupKey.ID}
		if len(groups) < batchLimit {
			break
		}
	}
	return entries, nil
}

func groupKnowledgeChangelogRows(pageRows []sqlc.ListFactChangelogBySubjectPageRow) [][]sqlc.CtxAgentMemoryChangelog {
	groups := make([][]sqlc.CtxAgentMemoryChangelog, 0, len(pageRows))
	var currentVersion int64
	for _, pageRow := range pageRows {
		row := serverFactChangelogPageRow(pageRow)
		if len(groups) == 0 || row.MemoryVersionAfter.Int64 != currentVersion {
			currentVersion = row.MemoryVersionAfter.Int64
			groups = append(groups, []sqlc.CtxAgentMemoryChangelog{})
		}
		groups[len(groups)-1] = append(groups[len(groups)-1], row)
	}
	return groups
}

type knowledgeChangelogFactState struct {
	row    sqlc.CtxAgentMemoryChangelog
	before *memory.Fact
	after  *memory.Fact
}

func (s *Server) projectKnowledgeChangelogGroup(ctx context.Context, userID string, agentID string, rows []sqlc.CtxAgentMemoryChangelog) (apiserver.ChangelogEntry, bool, error) {
	var active *knowledgeChangelogFactState
	var deprecated *knowledgeChangelogFactState
	for _, row := range rows {
		before, err := parseKnowledgeChangelogFact(row.BeforeText)
		if err != nil {
			return apiserver.ChangelogEntry{}, false, err
		}
		after, err := parseKnowledgeChangelogFact(row.AfterText)
		if err != nil {
			return apiserver.ChangelogEntry{}, false, err
		}
		if after == nil || after.Subject != memory.FactSubjectWorld {
			continue
		}
		state := knowledgeChangelogFactState{row: row, before: before, after: after}
		switch after.Status {
		case memory.FactStatusActive:
			active = &state
		case memory.FactStatusDeprecated:
			deprecated = &state
		}
	}

	if active != nil {
		entry := profileChangelogEntryToAPI(active.row)
		entry.Scope = "knowledge"
		entry.AfterText = stringPtrIfNotEmpty(active.after.Content)
		switch active.row.Action {
		case "replace":
			entry.Action = "edit"
			beforeText, found, err := s.replacedKnowledgeBeforeText(ctx, userID, agentID, active.after.Metadata)
			if err != nil {
				return apiserver.ChangelogEntry{}, false, err
			}
			if found {
				entry.BeforeText = stringPtrIfNotEmpty(beforeText)
			} else if deprecated != nil && deprecated.before != nil {
				entry.BeforeText = stringPtrIfNotEmpty(deprecated.before.Content)
			}
		case "create":
			entry.Action = "create"
			entry.BeforeText = nil
		case "restore":
			entry.Action = "restore"
			entry.BeforeText = nil
		default:
			return apiserver.ChangelogEntry{}, false, nil
		}
		return entry, true, nil
	}
	if deprecated == nil || deprecated.before == nil {
		return apiserver.ChangelogEntry{}, false, nil
	}
	action, ok, err := logicalKnowledgeDeprecationAction(deprecated.row.Metadata)
	if err != nil {
		return apiserver.ChangelogEntry{}, false, err
	}
	if !ok {
		return apiserver.ChangelogEntry{}, false, nil
	}
	entry := profileChangelogEntryToAPI(deprecated.row)
	entry.Scope = "knowledge"
	entry.Action = action
	entry.BeforeText = stringPtrIfNotEmpty(deprecated.before.Content)
	entry.AfterText = nil
	return entry, true, nil
}

func (s *Server) replacedKnowledgeBeforeText(ctx context.Context, userID string, agentID string, metadata json.RawMessage) (string, bool, error) {
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

	// Preserve replaced_fact_ids order because it is the writer's canonical
	// predecessor order; fact contents remain immutable after deprecation.
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

func logicalKnowledgeDeprecationAction(metadata pgtype.Text) (string, bool, error) {
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

func parseKnowledgeChangelogFact(text pgtype.Text) (*memory.Fact, error) {
	if !text.Valid || text.String == "" {
		return nil, nil
	}
	var fact memory.Fact
	if err := json.Unmarshal([]byte(text.String), &fact); err != nil {
		return nil, fmt.Errorf("parse knowledge changelog fact: %w", err)
	}
	return &fact, nil
}

func sortChangelogEntries(entries []apiserver.ChangelogEntry) {
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].CreatedAt.Equal(entries[j].CreatedAt) {
			return entries[i].Id > entries[j].Id
		}
		return entries[i].CreatedAt.After(entries[j].CreatedAt)
	})
}

func serverChangelogCursorParams(cursor *memory.ChangelogCursor) (pgtype.Timestamptz, pgtype.Text) {
	if cursor == nil {
		return pgtype.Timestamptz{}, pgtype.Text{}
	}
	return pgtype.Timestamptz{Time: cursor.CreatedAt.UTC(), Valid: true}, pgtype.Text{String: cursor.ID, Valid: true}
}

func serverFactChangelogPageRow(row sqlc.ListFactChangelogBySubjectPageRow) sqlc.CtxAgentMemoryChangelog {
	return sqlc.CtxAgentMemoryChangelog(row)
}
