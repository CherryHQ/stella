package skills

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

const managedSkillRestoreWindow = 2160 * time.Hour

var (
	// ErrSkillNotMutable rejects system, filesystem, and project lifecycle writes.
	ErrSkillNotMutable = errors.New("skill is not mutable")
	// ErrSkillRestoreExpired rejects restores at or after the exact 90-day deadline.
	ErrSkillRestoreExpired = errors.New("skill restore window has expired")
	// ErrSkillNameConflict keeps a retained row from displacing a live replacement.
	ErrSkillNameConflict = errors.New("skill name conflicts with a live skill")
)

// ListManagedSkills returns active rows or recoverable removed rows for mutable
// PostgreSQL scopes. The caller's owner fields are interpreted per row scope.
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
	if in.Now.IsZero() {
		in.Now = time.Now().UTC()
	} else {
		in.Now = in.Now.UTC()
	}
	createdBy := pgtype.Text{String: in.CreatedBy, Valid: in.CreatedBy != ""}
	query := pgtype.Text{String: in.Query, Valid: in.Query != ""}
	userID, agentID := managedOwnerParams(in.UserID, in.AgentID)
	limitPlusOne := in.Limit + 1
	cursorTimestamp, cursorID := managedSkillCursorParams(in.Cursor)

	switch in.State {
	case ManagedSkillStateActive:
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
	case ManagedSkillStateRemoved:
		total, err := s.q.CountManagedRemovedSkills(ctx, sqlc.CountManagedRemovedSkillsParams{
			Scopes: scopes, CreatedBy: createdBy, SearchQuery: query, NowAt: in.Now, UserID: userID, AgentID: agentID,
		})
		if err != nil {
			return ManagedSkillPage{}, fmt.Errorf("count managed removed skills: %w", err)
		}
		rows, err := s.q.ListManagedRemovedSkills(ctx, sqlc.ListManagedRemovedSkillsParams{
			Scopes: scopes, CreatedBy: createdBy, SearchQuery: query, NowAt: in.Now, UserID: userID, AgentID: agentID,
			CursorTimestamp: cursorTimestamp, CursorID: cursorID, LimitCount: limitPlusOne,
		})
		if err != nil {
			return ManagedSkillPage{}, fmt.Errorf("list managed removed skills: %w", err)
		}
		items := make([]ManagedSkillItem, 0, len(rows))
		for _, row := range rows {
			item, err := managedRemovedItem(row)
			if err != nil {
				return ManagedSkillPage{}, err
			}
			items = append(items, item)
		}
		return managedSkillPage(items, total, in.Limit), nil
	default:
		return ManagedSkillPage{}, fmt.Errorf("list managed skills: unsupported state %q", in.State)
	}
}

// DeprecateManagedSkill retains a live mutable row, preserving files and
// history. Reflect user-agent skills also retain usage data in the audit entry.
func (s *PGStore) DeprecateManagedSkill(ctx context.Context, in ManagedSkillDeprecate) (Skill, error) {
	if in.ID == "" || in.DeprecatedBy == "" {
		return Skill{}, fmt.Errorf("deprecate managed skill: id and deprecated_by are required")
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Skill{}, fmt.Errorf("begin managed skill deprecate: %w", err)
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

	metadata, usage, err := managedDeprecateMetadata(ctx, qtx, before, in.DeprecatedBy)
	if err != nil {
		return Skill{}, err
	}
	afterRow, err := qtx.DeprecateManagedSkill(ctx, before.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Skill{}, ErrSkillNotMutable
	}
	if err != nil {
		return Skill{}, fmt.Errorf("deprecate managed skill: %w", err)
	}
	after := mapRow(afterRow)
	if _, err := qtx.InsertSkillChangelog(ctx, skillChangelogParams(before, after, "deprecate", metadata)); err != nil {
		return Skill{}, fmt.Errorf("record managed skill deprecation: %w", err)
	}
	if usage != nil {
		if err := qtx.DeleteSkillUsage(ctx, before.ID); err != nil {
			return Skill{}, fmt.Errorf("delete reflect skill usage: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Skill{}, fmt.Errorf("commit managed skill deprecation: %w", err)
	}
	return after, nil
}

// RestoreManagedSkill restores a qualifying deprecated row in place. A live
// row is an idempotent no-op, while the recovery deadline is exclusive.
func (s *PGStore) RestoreManagedSkill(ctx context.Context, in ManagedSkillRestore) (ManagedSkillRestoreResult, error) {
	if in.ID == "" || in.RestoredBy == "" {
		return ManagedSkillRestoreResult{}, fmt.Errorf("restore managed skill: id and restored_by are required")
	}
	if in.Now.IsZero() {
		in.Now = time.Now().UTC()
	} else {
		in.Now = in.Now.UTC()
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return ManagedSkillRestoreResult{}, fmt.Errorf("begin managed skill restore: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)

	before, err := lockedManagedSkill(ctx, qtx, in.ID, in.Scope, in.UserID, in.AgentID)
	if err != nil {
		if errors.Is(err, ErrSkillNotMutable) {
			return ManagedSkillRestoreResult{}, err
		}
		return ManagedSkillRestoreResult{}, ErrSkillNotRestorable
	}
	if before.Status == "active" {
		if err := tx.Commit(ctx); err != nil {
			return ManagedSkillRestoreResult{}, fmt.Errorf("commit managed skill restore no-op: %w", err)
		}
		return ManagedSkillRestoreResult{Skill: before}, nil
	}
	if before.Status != "deprecated" {
		return ManagedSkillRestoreResult{}, ErrSkillNotRestorable
	}

	deprecateLog, err := qtx.GetLatestQualifyingManagedSkillDeprecateChangelog(ctx, before.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ManagedSkillRestoreResult{}, ErrSkillNotRestorable
	}
	if err != nil {
		return ManagedSkillRestoreResult{}, fmt.Errorf("read managed skill deprecation: %w", err)
	}
	if !in.Now.Before(deprecateLog.CreatedAt.UTC().Add(managedSkillRestoreWindow)) {
		return ManagedSkillRestoreResult{}, ErrSkillRestoreExpired
	}
	conflict, err := qtx.HasLiveManagedSkillNameConflict(ctx, liveNameConflictParams(before))
	if err != nil {
		return ManagedSkillRestoreResult{}, fmt.Errorf("check managed skill name conflict: %w", err)
	}
	if conflict {
		return ManagedSkillRestoreResult{}, ErrSkillNameConflict
	}

	afterRow, err := qtx.RestoreManagedSkill(ctx, before.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ManagedSkillRestoreResult{}, ErrSkillNotRestorable
	}
	if err != nil {
		return ManagedSkillRestoreResult{}, managedRestoreUpdateError(err)
	}
	after := mapRow(afterRow)
	if IsReflectOwned(before) && before.Scope == "user_agent" {
		if err := qtx.UpsertSkillUsageOnReflectRestore(ctx, sqlc.UpsertSkillUsageOnReflectRestoreParams{
			SkillID: after.ID, UserID: before.UserID, AgentID: before.AgentID,
			UseCount: restoreSkillUseCount(deprecateLog.Metadata), LastUsedAt: restoreSkillLastUsedAt(deprecateLog.Metadata),
		}); err != nil {
			return ManagedSkillRestoreResult{}, fmt.Errorf("restore managed reflect usage: %w", err)
		}
	}
	if _, err := qtx.InsertSkillChangelog(ctx, skillChangelogParams(before, after, "restore", managedRestoreMetadata(in.RestoredBy, deprecateLog))); err != nil {
		return ManagedSkillRestoreResult{}, fmt.Errorf("record managed skill restore: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return ManagedSkillRestoreResult{}, fmt.Errorf("commit managed skill restore: %w", err)
	}
	return ManagedSkillRestoreResult{Skill: after, Restored: true}, nil
}

// managedRestoreUpdateError closes the interval between the name-conflict
// precheck and UPDATE. PostgreSQL remains the final concurrency authority.
func managedRestoreUpdateError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "idx_skill_owner_name" {
		return ErrSkillNameConflict
	}
	return fmt.Errorf("restore managed skill: %w", err)
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
		return Skill{}, ErrSkillNotRestorable
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
		sortTimestamp := last.Skill.UpdatedAt
		if last.DeprecatedAt != nil {
			sortTimestamp = *last.DeprecatedAt
		}
		page.NextCursor = &ManagedSkillCursor{Timestamp: sortTimestamp, ID: last.Skill.ID}
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

func managedRemovedItem(row sqlc.ListManagedRemovedSkillsRow) (ManagedSkillItem, error) {
	removalSource, err := managedRemovalSource(row.DeprecateMetadata)
	if err != nil {
		return ManagedSkillItem{}, err
	}
	deprecatedAt := row.DeprecatedAt.UTC()
	deadline := deprecatedAt.Add(managedSkillRestoreWindow)
	sk := Skill{
		ID: row.ID, Scope: row.Scope, UserID: row.UserID.String, AgentID: row.AgentID.String,
		Name: row.Name, Description: row.Description, Status: row.Status, DisableModelInvocation: row.DisableModelInvocation,
		Metadata: row.Metadata, CreatedAt: row.CreatedAt.UTC(), UpdatedAt: row.UpdatedAt.UTC(), Version: row.Version,
	}
	return ManagedSkillItem{
		Skill: sk, CreatedBy: CreatedBy(sk), RemovalSource: removalSource,
		DeprecatedAt: &deprecatedAt, RestoreDeadline: &deadline, IsRestorable: true,
	}, nil
}

func managedRemovalSource(metadata json.RawMessage) (string, error) {
	fields, ok := skillMetadataFields(metadata)
	if !ok {
		return "", ErrSkillNotRestorable
	}
	if deprecatedBy, _ := fields["deprecated_by"].(string); deprecatedBy == "manual" {
		return "manual", nil
	}
	if curator, _ := fields["curator"].(string); curator == "usage" {
		return "curator", nil
	}
	return "", ErrSkillNotRestorable
}

func managedDeprecateMetadata(ctx context.Context, q *sqlc.Queries, sk Skill, deprecatedBy string) (json.RawMessage, *sqlc.SkillUsage, error) {
	payload := map[string]any{"deprecated_by": "manual", "deprecated_by_user_id": deprecatedBy}
	if !IsReflectOwned(sk) || sk.Scope != "user_agent" {
		metadata, err := json.Marshal(payload)
		return metadata, nil, err
	}
	usage, err := q.GetSkillUsageForUpdate(ctx, sqlc.GetSkillUsageForUpdateParams{SkillID: sk.ID, UserID: sk.UserID, AgentID: sk.AgentID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, ErrSkillNotRestorable
	}
	if err != nil {
		return nil, nil, fmt.Errorf("lock reflect skill usage: %w", err)
	}
	payload["use_count"] = usage.UseCount
	payload["last_used_at"] = usage.LastUsedAt.UTC().Format(time.RFC3339)
	metadata, err := json.Marshal(payload)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal managed skill deprecation metadata: %w", err)
	}
	return metadata, &usage, nil
}

func managedRestoreMetadata(restoredBy string, deprecated sqlc.SkillChangelog) json.RawMessage {
	payload := map[string]any{
		"restored_by":             restoredBy,
		"deprecated_changelog_id": deprecated.ID,
		"deprecated_at":           deprecated.CreatedAt.UTC().Format(time.RFC3339),
	}
	metadata, err := json.Marshal(payload)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return metadata
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

func liveNameConflictParams(sk Skill) sqlc.HasLiveManagedSkillNameConflictParams {
	userID, agentID := managedOwnerParams(sk.UserID, sk.AgentID)
	return sqlc.HasLiveManagedSkillNameConflictParams{ID: sk.ID, Name: sk.Name, Scope: sk.Scope, UserID: userID, AgentID: agentID}
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
	DeprecateManagedSkill(context.Context, ManagedSkillDeprecate) (Skill, error)
	RestoreManagedSkill(context.Context, ManagedSkillRestore) (ManagedSkillRestoreResult, error)
	UpdateManagedSkill(context.Context, ManagedSkillUpdate) (Skill, error)
} = (*PGStore)(nil)
