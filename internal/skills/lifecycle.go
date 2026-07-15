package skills

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// ErrSkillNotMutable rejects system, filesystem, deprecated, and project writes.
var ErrSkillNotMutable = errors.New("skill is not mutable")

// ListManagedSkills returns active rows for mutable PostgreSQL scopes. The
// caller's owner fields are interpreted per row scope.
func (s *PGStore) ListManagedSkills(ctx context.Context, in ManagedSkillListQuery) (ManagedSkillPage, error) {
	if in.Limit <= 0 {
		return ManagedSkillPage{}, fmt.Errorf("list managed skills: limit must be positive")
	}
	if err := validateManagedSkillCursor(in.Cursor); err != nil {
		return ManagedSkillPage{}, err
	}
	scopes, err := managedScopes(in.Scopes)
	if err != nil {
		return ManagedSkillPage{}, err
	}
	createdBy := pgtype.Text{String: in.CreatedBy, Valid: in.CreatedBy != ""}
	query := pgtype.Text{String: in.Query, Valid: in.Query != ""}
	userID, agentID := managedOwnerParams(in.UserID, in.AgentID)
	limitPlusOne := in.Limit + 1
	cursorTimestamp, cursorID := managedSkillCursorParams(in.Cursor)

	total, err := s.q.CountManagedActiveSkills(ctx, sqlc.CountManagedActiveSkillsParams{
		Scopes: scopes, CreatedBy: createdBy, SearchQuery: query, UserID: userID, AgentID: agentID,
	})
	if err != nil {
		return ManagedSkillPage{}, fmt.Errorf("count managed active skills: %w", err)
	}
	rows, err := s.q.ListManagedActiveSkills(ctx, sqlc.ListManagedActiveSkillsParams{
		Scopes: scopes, CreatedBy: createdBy, SearchQuery: query, UserID: userID, AgentID: agentID,
		CursorTimestamp: cursorTimestamp, CursorID: cursorID, LimitCount: limitPlusOne,
	})
	if err != nil {
		return ManagedSkillPage{}, fmt.Errorf("list managed active skills: %w", err)
	}
	items := make([]ManagedSkillItem, 0, len(rows))
	for _, row := range rows {
		sk := mapRow(row)
		items = append(items, ManagedSkillItem{Skill: sk, CreatedBy: CreatedBy(sk)})
	}
	return managedSkillPage(items, total, in.Limit), nil
}

// UpdateManagedSkill atomically applies file upserts, metadata changes, an
// ownership transfer, and a changelog entry while the row lock is held.
func (s *PGStore) UpdateManagedSkill(ctx context.Context, in ManagedSkillUpdate) (Skill, error) {
	if in.ID == "" {
		return Skill{}, fmt.Errorf("update managed skill: id is required")
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Skill{}, fmt.Errorf("begin managed skill update: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)

	before, err := lockedManagedSkill(ctx, qtx, in.ID, in.Scope, in.UserID, in.AgentID)
	if err != nil {
		return Skill{}, err
	}
	if before.Status == "deprecated" {
		return Skill{}, ErrSkillNotMutable
	}
	if in.Patch.Status != nil && *in.Patch.Status == "deprecated" {
		return Skill{}, ErrSkillNotMutable
	}
	if in.ConvertToManual && !IsReflectOwned(before) {
		return Skill{}, ErrSkillNotReflectOwned
	}

	patch := applyPatch(skillToRow(before), in.Patch)
	metadata, err := managedUpdateMetadata(patch.Metadata, CreatedBy(before), in.ConvertToManual)
	if err != nil {
		return Skill{}, err
	}
	for path, content := range in.Files {
		if err := qtx.UpsertSkillFile(ctx, sqlc.UpsertSkillFileParams{SkillID: before.ID, Path: path, Content: content}); err != nil {
			return Skill{}, fmt.Errorf("update managed skill file %q: %w", path, err)
		}
	}
	for _, path := range in.DeleteFiles {
		if err := qtx.DeleteSkillFile(ctx, sqlc.DeleteSkillFileParams{SkillID: before.ID, Path: path}); err != nil {
			return Skill{}, fmt.Errorf("delete managed skill file %q: %w", path, err)
		}
	}
	afterRow, err := qtx.UpdateManagedSkill(ctx, sqlc.UpdateManagedSkillParams{
		ID: before.ID, Description: patch.Description, Status: patch.Status,
		DisableModelInvocation: patch.DisableModelInvocation, Metadata: metadata,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Skill{}, ErrSkillNotMutable
	}
	if err != nil {
		return Skill{}, fmt.Errorf("update managed skill: %w", err)
	}
	after := mapRow(afterRow)
	if in.ConvertToManual && before.Scope == "user_agent" {
		if err := qtx.DeleteSkillUsage(ctx, before.ID); err != nil {
			return Skill{}, fmt.Errorf("delete converted reflect skill usage: %w", err)
		}
	}
	if _, err := qtx.InsertSkillChangelog(ctx, skillChangelogParams(before, after, "patch", json.RawMessage(`{}`))); err != nil {
		return Skill{}, fmt.Errorf("record managed skill update: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Skill{}, fmt.Errorf("commit managed skill update: %w", err)
	}
	return after, nil
}

func managedScopes(scopes []string) ([]string, error) {
	if len(scopes) == 0 {
		return []string{"user", "user_agent", "system_agent"}, nil
	}
	for _, scope := range scopes {
		if !isMutableSkillScope(scope) {
			return nil, ErrSkillNotMutable
		}
	}
	return scopes, nil
}

func lockedManagedSkill(ctx context.Context, q *sqlc.Queries, id, scope, userID, agentID string) (Skill, error) {
	if !isMutableSkillScope(scope) {
		return Skill{}, ErrSkillNotMutable
	}
	row, err := q.GetSkillForUpdate(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Skill{}, ErrSkillNotMutable
	}
	if err != nil {
		return Skill{}, fmt.Errorf("lock managed skill: %w", err)
	}
	sk := mapRow(row)
	if sk.Scope != scope || !managedOwnerMatches(sk, userID, agentID) {
		return Skill{}, ErrSkillNotMutable
	}
	return sk, nil
}

func isMutableSkillScope(scope string) bool {
	return scope == "user" || scope == "user_agent" || scope == "system_agent"
}

func managedOwnerMatches(sk Skill, userID, agentID string) bool {
	switch sk.Scope {
	case "user":
		return userID != "" && sk.UserID == userID
	case "user_agent":
		return userID != "" && agentID != "" && sk.UserID == userID && sk.AgentID == agentID
	case "system_agent":
		return agentID != "" && sk.AgentID == agentID
	default:
		return false
	}
}

func managedOwnerParams(userID, agentID string) (pgtype.Text, pgtype.Text) {
	return pgtype.Text{String: userID, Valid: userID != ""}, pgtype.Text{String: agentID, Valid: agentID != ""}
}

func managedSkillPage(items []ManagedSkillItem, total int64, limit int32) ManagedSkillPage {
	page := ManagedSkillPage{Items: items, Total: total}
	if int32(len(page.Items)) > limit {
		page.Items = page.Items[:limit]
		page.HasMore = true
	}
	if len(page.Items) > 0 {
		last := page.Items[len(page.Items)-1]
		page.NextCursor = &ManagedSkillCursor{Timestamp: last.Skill.UpdatedAt, ID: last.Skill.ID}
	}
	return page
}

func validateManagedSkillCursor(cursor *ManagedSkillCursor) error {
	if cursor != nil && (cursor.Timestamp.IsZero() || cursor.ID == "") {
		return fmt.Errorf("list managed skills: cursor timestamp and id must be provided together")
	}
	return nil
}

func managedSkillCursorParams(cursor *ManagedSkillCursor) (pgtype.Timestamptz, pgtype.Text) {
	if cursor == nil {
		return pgtype.Timestamptz{}, pgtype.Text{}
	}
	return pgtype.Timestamptz{Time: cursor.Timestamp.UTC(), Valid: true}, pgtype.Text{String: cursor.ID, Valid: true}
}

func managedUpdateMetadata(metadata json.RawMessage, existingCreatedBy string, convertToManual bool) (json.RawMessage, error) {
	fields := map[string]any{}
	if len(metadata) > 0 && string(metadata) != "null" {
		if err := json.Unmarshal(metadata, &fields); err != nil {
			return nil, fmt.Errorf("decode managed skill metadata: %w", err)
		}
	}
	if fields == nil {
		fields = map[string]any{}
	}
	switch {
	case convertToManual:
		fields[reflectSkillCreatedByKey] = ManualSkillCreatedBy
	case existingCreatedBy == "":
		delete(fields, reflectSkillCreatedByKey)
	default:
		fields[reflectSkillCreatedByKey] = existingCreatedBy
	}
	out, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("encode managed skill metadata: %w", err)
	}
	return json.RawMessage(out), nil
}

func skillChangelogParams(before Skill, after Skill, action string, metadata json.RawMessage) sqlc.InsertSkillChangelogParams {
	userID, agentID := managedOwnerParams(before.UserID, before.AgentID)
	return sqlc.InsertSkillChangelogParams{
		SkillID: before.ID, UserID: userID, AgentID: agentID, Scope: before.Scope, Action: action,
		VersionBefore: pgtype.Int8{Int64: before.Version, Valid: true}, VersionAfter: after.Version, Metadata: metadata,
	}
}

// skillToRow supplies the existing patch helper without exposing SQL storage
// mechanics to lifecycle callers.
func skillToRow(sk Skill) sqlc.Skill {
	userID, agentID := managedOwnerParams(sk.UserID, sk.AgentID)
	return sqlc.Skill{
		ID: sk.ID, Scope: sk.Scope, UserID: userID, AgentID: agentID, Name: sk.Name, Description: sk.Description,
		Status: sk.Status, DisableModelInvocation: sk.DisableModelInvocation, Metadata: sk.Metadata,
		CreatedAt: sk.CreatedAt, UpdatedAt: sk.UpdatedAt, Version: sk.Version,
	}
}

// Compile-time assertion that PGStore exposes the lifecycle Store contract.
var _ interface {
	ListManagedSkills(context.Context, ManagedSkillListQuery) (ManagedSkillPage, error)
	UpdateManagedSkill(context.Context, ManagedSkillUpdate) (Skill, error)
} = (*PGStore)(nil)
