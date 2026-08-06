package skills

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// ErrSkillNotMutable rejects system, filesystem, deprecated, and project writes.
var ErrSkillNotMutable = errors.New("skill is not mutable")

// ErrInvalidSkillFilePath rejects keys whose runtime path would differ from the
// canonical relative path stored in PostgreSQL.
var ErrInvalidSkillFilePath = errors.New("invalid skill file path")

// UpdateManagedSkill atomically applies file upserts, metadata changes, an
// ownership transfer, and a changelog entry while the row lock is held.
func (s *PGStore) UpdateManagedSkill(ctx context.Context, in ManagedSkillUpdate) (SkillSnapshot, error) {
	if in.ID == "" {
		return SkillSnapshot{}, fmt.Errorf("update managed skill: id is required")
	}
	if err := validateManagedSkillFileChanges(in.Files, in.DeleteFiles); err != nil {
		return SkillSnapshot{}, err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return SkillSnapshot{}, fmt.Errorf("begin managed skill update: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)

	before, err := lockedManagedSkill(ctx, qtx, in.ID, in.Scope, in.UserID, in.AgentID)
	if err != nil {
		return SkillSnapshot{}, err
	}
	if before.Status == "deprecated" {
		return SkillSnapshot{}, ErrSkillNotMutable
	}
	if in.ConvertToManual && !IsReflectOwned(before) {
		return SkillSnapshot{}, ErrSkillNotReflectOwned
	}

	patch := applyPatch(skillToRow(before), in.Patch)
	metadata, err := managedUpdateMetadata(patch.Metadata, CreatedBy(before), in.ConvertToManual)
	if err != nil {
		return SkillSnapshot{}, err
	}
	for path, content := range in.Files {
		if err := qtx.UpsertSkillFile(ctx, sqlc.UpsertSkillFileParams{SkillID: before.ID, Path: path, Content: []byte(content)}); err != nil {
			return SkillSnapshot{}, fmt.Errorf("update managed skill file %q: %w", path, err)
		}
	}
	for _, path := range in.DeleteFiles {
		if err := qtx.DeleteSkillFile(ctx, sqlc.DeleteSkillFileParams{SkillID: before.ID, Path: path}); err != nil {
			return SkillSnapshot{}, fmt.Errorf("delete managed skill file %q: %w", path, err)
		}
	}
	afterRow, err := qtx.UpdateManagedSkill(ctx, sqlc.UpdateManagedSkillParams{
		ID: before.ID, Description: patch.Description, Status: patch.Status,
		DisableModelInvocation: patch.DisableModelInvocation, Metadata: metadata,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return SkillSnapshot{}, ErrSkillNotMutable
	}
	if err != nil {
		return SkillSnapshot{}, fmt.Errorf("update managed skill: %w", err)
	}
	after := mapRow(afterRow)
	if in.ConvertToManual && before.Scope == "user_agent" {
		if err := qtx.DeleteSkillUsage(ctx, before.ID); err != nil {
			return SkillSnapshot{}, fmt.Errorf("delete converted reflect skill usage: %w", err)
		}
	}
	if _, err := qtx.InsertSkillChangelog(ctx, skillChangelogParams(before, after, "patch", json.RawMessage(`{}`))); err != nil {
		return SkillSnapshot{}, fmt.Errorf("record managed skill update: %w", err)
	}
	fileRows, err := qtx.ListSkillFiles(ctx, before.ID)
	if err != nil {
		return SkillSnapshot{}, fmt.Errorf("list committed managed skill files: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return SkillSnapshot{}, fmt.Errorf("commit managed skill update: %w", err)
	}
	files := make([]string, 0, len(fileRows))
	for _, file := range fileRows {
		files = append(files, file.Path)
	}
	sort.Strings(files)
	return SkillSnapshot{Skill: after, Files: files}, nil
}

// DeleteManagedSkill preserves the legacy PG delete semantics while proving
// supplied scope and owner facts under one row lock. ExpectedDigest is ignored
// until Home is the composed content authority.
func (s *PGStore) DeleteManagedSkill(ctx context.Context, in ManagedSkillDelete) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin managed skill delete: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	before, err := lockedManagedSkill(ctx, qtx, in.ID, in.Scope, in.UserID, in.AgentID)
	if err != nil {
		return err
	}
	userID, agentID := managedOwnerParams(before.UserID, before.AgentID)
	if err := qtx.DeleteSkill(ctx, sqlc.DeleteSkillParams{ID: before.ID, UserID: userID, AgentID: agentID}); err != nil {
		return fmt.Errorf("delete managed skill: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit managed skill delete: %w", err)
	}
	return nil
}

// DeleteManagedSkillFile removes one companion file under the same row lock as
// its scope/owner validation and returns the retained committed snapshot.
func (s *PGStore) DeleteManagedSkillFile(ctx context.Context, in ManagedSkillFileDelete) (SkillSnapshot, error) {
	if err := validateSkillDeletePaths([]string{in.Path}); err != nil {
		return SkillSnapshot{}, err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return SkillSnapshot{}, fmt.Errorf("begin managed skill file delete: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	before, err := lockedManagedSkill(ctx, qtx, in.ID, in.Scope, in.UserID, in.AgentID)
	if err != nil {
		return SkillSnapshot{}, err
	}
	if err := qtx.DeleteSkillFile(ctx, sqlc.DeleteSkillFileParams{SkillID: before.ID, Path: in.Path}); err != nil {
		return SkillSnapshot{}, fmt.Errorf("delete managed skill file %q: %w", in.Path, err)
	}
	rows, err := qtx.ListSkillFiles(ctx, before.ID)
	if err != nil {
		return SkillSnapshot{}, fmt.Errorf("list committed managed skill files: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return SkillSnapshot{}, fmt.Errorf("commit managed skill file delete: %w", err)
	}
	files := make([]string, 0, len(rows))
	for _, row := range rows {
		files = append(files, row.Path)
	}
	sort.Strings(files)
	return SkillSnapshot{Skill: before, Files: files}, nil
}

func validateSkillFilePaths(files map[string]string) error {
	for raw := range files {
		clean := path.Clean(raw)
		if raw == "" || strings.ContainsRune(raw, '\x00') || strings.Contains(raw, "\\") || path.IsAbs(raw) || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || clean != raw {
			return fmt.Errorf("%w: %q must be a canonical relative path", ErrInvalidSkillFilePath, raw)
		}
	}
	return nil
}

func validateSkillDeletePaths(paths []string) error {
	for _, path := range paths {
		if path == MainFile {
			return errors.New("skills: SKILL.md cannot be deleted")
		}
		if err := validateSkillFilePaths(map[string]string{path: ""}); err != nil {
			return err
		}
	}
	return nil
}

var ErrInvalidManagedSkillFileMutation = errors.New("invalid managed skill file mutation")

func validateManagedSkillFileChanges(files map[string]string, deleteFiles []string) error {
	if err := validateSkillFilePaths(files); err != nil {
		return err
	}
	if err := validateSkillDeletePaths(deleteFiles); err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(deleteFiles))
	for _, path := range deleteFiles {
		if _, duplicate := seen[path]; duplicate {
			return fmt.Errorf("%w: duplicate delete path %q", ErrInvalidManagedSkillFileMutation, path)
		}
		seen[path] = struct{}{}
		if _, both := files[path]; both {
			return fmt.Errorf("%w: path %q is both upserted and deleted", ErrInvalidManagedSkillFileMutation, path)
		}
	}
	return nil
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
