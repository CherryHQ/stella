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
