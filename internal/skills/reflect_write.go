package skills

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

var validReflectSkillNameRe = regexp.MustCompile(`^[a-z0-9-]+$`)

var (
	ErrSkillVersionConflict  = errors.New("skill version conflict")
	ErrSkillNotReflectOwned  = errors.New("skill is not reflect-owned")
	ErrSkillUsageChanged     = errors.New("skill usage changed")
	ErrSkillNotRestorable    = errors.New("skill is not restorable")
	ErrSkillRestoreBadCaller = errors.New("skill restore requires restored_by")
)

type ReflectSkillCreate struct {
	UserID          string
	AgentID         string
	Name            string
	Description     string
	MainFileContent string
	Metadata        json.RawMessage
}

type ReflectSkillPatch struct {
	ID                     string
	UserID                 string
	AgentID                string
	ExpectedVersion        int64
	Description            *string
	Status                 *string
	DisableModelInvocation *bool
	MainFileContent        *string
	Metadata               json.RawMessage
}

type ReflectSkillDeprecate struct {
	ID                      string
	UserID                  string
	AgentID                 string
	ExpectedVersion         int64
	ExpectedUsageLastUsedAt *time.Time
	Metadata                json.RawMessage
}

type ReflectSkillRestore struct {
	ID         string
	UserID     string
	AgentID    string
	RestoredBy string
	Reason     string
}

type ReflectSkillRestoreResult struct {
	Skill    Skill
	Restored bool
}

// CreateReflectOwnedUserAgentSkill creates an active Reflect-owned user_agent
// skill and records the initial version in skill_changelog.
func (s *PGStore) CreateReflectOwnedUserAgentSkill(ctx context.Context, in ReflectSkillCreate) (Skill, error) {
	if err := validateReflectSkillName(in.Name); err != nil {
		return Skill{}, err
	}
	if in.UserID == "" || in.AgentID == "" {
		return Skill{}, fmt.Errorf("skills: user_id and agent_id are required")
	}
	if in.MainFileContent == "" {
		return Skill{}, fmt.Errorf("skills: SKILL.md content is required")
	}

	metadata, err := MarkReflectOwnedMetadata(in.Metadata)
	if err != nil {
		return Skill{}, err
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Skill{}, fmt.Errorf("skills: begin reflect create: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)

	row, err := qtx.CreateSkill(ctx, sqlc.CreateSkillParams{
		ID:          uuid.New().String()[:8],
		Scope:       "user_agent",
		UserID:      pgtype.Text{String: in.UserID, Valid: true},
		AgentID:     pgtype.Text{String: in.AgentID, Valid: true},
		Name:        in.Name,
		Description: in.Description,
		Status:      "active",
		Metadata:    metadata,
	})
	if err != nil {
		return Skill{}, fmt.Errorf("skills: create reflect-owned skill %q: %w", in.Name, err)
	}
	if err := qtx.UpsertSkillFile(ctx, sqlc.UpsertSkillFileParams{
		SkillID: row.ID,
		Path:    MainFile,
		Content: in.MainFileContent,
	}); err != nil {
		return Skill{}, fmt.Errorf("skills: create reflect-owned SKILL.md: %w", err)
	}

	skill := mapRow(row)
	if err := qtx.UpsertSkillUsageOnReflectCreate(ctx, sqlc.UpsertSkillUsageOnReflectCreateParams{
		SkillID: skill.ID,
		UserID:  in.UserID,
		AgentID: in.AgentID,
	}); err != nil {
		return Skill{}, fmt.Errorf("skills: initialize reflect skill usage: %w", err)
	}
	if _, err := qtx.InsertSkillChangelog(ctx, sqlc.InsertSkillChangelogParams{
		SkillID:      skill.ID,
		UserID:       pgtype.Text{String: in.UserID, Valid: true},
		AgentID:      pgtype.Text{String: in.AgentID, Valid: true},
		Scope:        skill.Scope,
		Action:       "create",
		VersionAfter: skill.Version,
		Metadata:     json.RawMessage(`{}`),
	}); err != nil {
		return Skill{}, fmt.Errorf("skills: record reflect create changelog: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return Skill{}, fmt.Errorf("skills: commit reflect create: %w", err)
	}
	return skill, nil
}

// PatchReflectOwnedUserAgentSkill updates a Reflect-owned user_agent skill under
// row lock. The expected version prevents stale reconciliation plans from
// overwriting a newer skill body.
func (s *PGStore) PatchReflectOwnedUserAgentSkill(ctx context.Context, in ReflectSkillPatch) (Skill, error) {
	if in.ID == "" || in.UserID == "" || in.AgentID == "" {
		return Skill{}, fmt.Errorf("skills: id, user_id, and agent_id are required")
	}
	if in.ExpectedVersion <= 0 {
		return Skill{}, fmt.Errorf("skills: expected_version is required")
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Skill{}, fmt.Errorf("skills: begin reflect patch: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)

	beforeRow, err := qtx.GetSkillForUpdate(ctx, in.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Skill{}, fmt.Errorf("skills: skill %q not found: %w", in.ID, pgx.ErrNoRows)
	}
	if err != nil {
		return Skill{}, fmt.Errorf("skills: lock reflect-owned skill: %w", err)
	}
	before := mapRow(beforeRow)
	if before.Scope != "user_agent" || before.UserID != in.UserID || before.AgentID != in.AgentID || !IsReflectOwned(before) {
		return Skill{}, ErrSkillNotReflectOwned
	}
	if before.Version != in.ExpectedVersion {
		return Skill{}, ErrSkillVersionConflict
	}

	description := before.Description
	if in.Description != nil {
		description = *in.Description
	}
	status := before.Status
	if in.Status != nil {
		status = *in.Status
	}
	disable := before.DisableModelInvocation
	if in.DisableModelInvocation != nil {
		disable = *in.DisableModelInvocation
	}
	metadata := before.Metadata
	if len(in.Metadata) > 0 {
		metadata, err = MarkReflectOwnedMetadata(in.Metadata)
		if err != nil {
			return Skill{}, err
		}
	}

	if in.MainFileContent != nil {
		if err := qtx.UpsertSkillFile(ctx, sqlc.UpsertSkillFileParams{
			SkillID: in.ID,
			Path:    MainFile,
			Content: *in.MainFileContent,
		}); err != nil {
			return Skill{}, fmt.Errorf("skills: patch reflect-owned SKILL.md: %w", err)
		}
	}

	afterRow, err := qtx.UpdateReflectOwnedUserAgentSkill(ctx, sqlc.UpdateReflectOwnedUserAgentSkillParams{
		ID:                     in.ID,
		UserID:                 in.UserID,
		AgentID:                in.AgentID,
		ExpectedVersion:        in.ExpectedVersion,
		Description:            description,
		Status:                 status,
		DisableModelInvocation: disable,
		Metadata:               metadata,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Skill{}, ErrSkillVersionConflict
	}
	if err != nil {
		return Skill{}, fmt.Errorf("skills: patch reflect-owned metadata: %w", err)
	}
	after := mapRow(afterRow)
	if err := qtx.RefreshSkillUsageOnReflectPatch(ctx, sqlc.RefreshSkillUsageOnReflectPatchParams{
		SkillID: after.ID,
		UserID:  in.UserID,
		AgentID: in.AgentID,
	}); err != nil {
		return Skill{}, fmt.Errorf("skills: refresh reflect skill usage: %w", err)
	}

	if _, err := qtx.InsertSkillChangelog(ctx, sqlc.InsertSkillChangelogParams{
		SkillID:       after.ID,
		UserID:        pgtype.Text{String: in.UserID, Valid: true},
		AgentID:       pgtype.Text{String: in.AgentID, Valid: true},
		Scope:         after.Scope,
		Action:        "patch",
		VersionBefore: pgtype.Int8{Int64: before.Version, Valid: true},
		VersionAfter:  after.Version,
		Metadata:      json.RawMessage(`{}`),
	}); err != nil {
		return Skill{}, fmt.Errorf("skills: record reflect patch changelog: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return Skill{}, fmt.Errorf("skills: commit reflect patch: %w", err)
	}
	return after, nil
}

// DeprecateReflectOwnedUserAgentSkill marks a Reflect-owned user_agent skill as
// deprecated under optimistic version control and removes its usage row.
func (s *PGStore) DeprecateReflectOwnedUserAgentSkill(ctx context.Context, in ReflectSkillDeprecate) (Skill, error) {
	if in.ID == "" || in.UserID == "" || in.AgentID == "" {
		return Skill{}, fmt.Errorf("skills: id, user_id, and agent_id are required")
	}
	if in.ExpectedVersion <= 0 {
		return Skill{}, fmt.Errorf("skills: expected_version is required")
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Skill{}, fmt.Errorf("skills: begin reflect deprecate: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)

	beforeRow, err := qtx.GetSkillForUpdate(ctx, in.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Skill{}, fmt.Errorf("skills: skill %q not found: %w", in.ID, pgx.ErrNoRows)
	}
	if err != nil {
		return Skill{}, fmt.Errorf("skills: lock reflect-owned skill: %w", err)
	}
	before := mapRow(beforeRow)
	if before.Scope != "user_agent" || before.UserID != in.UserID || before.AgentID != in.AgentID || !IsReflectOwned(before) {
		return Skill{}, ErrSkillNotReflectOwned
	}
	if before.Version != in.ExpectedVersion || before.Status != SkillStatusActive {
		return Skill{}, ErrSkillVersionConflict
	}
	if in.ExpectedUsageLastUsedAt != nil {
		usage, err := qtx.GetSkillUsageForUpdate(ctx, sqlc.GetSkillUsageForUpdateParams{
			SkillID: in.ID,
			UserID:  in.UserID,
			AgentID: in.AgentID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return Skill{}, ErrSkillUsageChanged
		}
		if err != nil {
			return Skill{}, fmt.Errorf("skills: lock reflect skill usage: %w", err)
		}
		if !usage.LastUsedAt.Equal(*in.ExpectedUsageLastUsedAt) {
			return Skill{}, ErrSkillUsageChanged
		}
	}

	afterRow, err := qtx.DeprecateReflectOwnedUserAgentSkill(ctx, sqlc.DeprecateReflectOwnedUserAgentSkillParams{
		ID:              in.ID,
		UserID:          in.UserID,
		AgentID:         in.AgentID,
		ExpectedVersion: in.ExpectedVersion,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Skill{}, ErrSkillVersionConflict
	}
	if err != nil {
		return Skill{}, fmt.Errorf("skills: deprecate reflect-owned skill: %w", err)
	}
	after := mapRow(afterRow)
	if err := qtx.DeleteSkillUsage(ctx, after.ID); err != nil {
		return Skill{}, fmt.Errorf("skills: delete deprecated skill usage: %w", err)
	}

	metadata := in.Metadata
	if len(metadata) == 0 {
		metadata = json.RawMessage(`{}`)
	}
	if _, err := qtx.InsertSkillChangelog(ctx, sqlc.InsertSkillChangelogParams{
		SkillID:       after.ID,
		UserID:        pgtype.Text{String: in.UserID, Valid: true},
		AgentID:       pgtype.Text{String: in.AgentID, Valid: true},
		Scope:         after.Scope,
		Action:        "deprecate",
		VersionBefore: pgtype.Int8{Int64: before.Version, Valid: true},
		VersionAfter:  after.Version,
		Metadata:      metadata,
	}); err != nil {
		return Skill{}, fmt.Errorf("skills: record reflect deprecate changelog: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return Skill{}, fmt.Errorf("skills: commit reflect deprecate: %w", err)
	}
	return after, nil
}

// RestoreReflectOwnedUserAgentSkill restores a usage-curator-deprecated
// Reflect-owned user_agent skill. It is not part of the plugin-facing tool
// surface; callers must be internal/admin code.
func (s *PGStore) RestoreReflectOwnedUserAgentSkill(ctx context.Context, in ReflectSkillRestore) (ReflectSkillRestoreResult, error) {
	if in.ID == "" || in.UserID == "" || in.AgentID == "" {
		return ReflectSkillRestoreResult{}, fmt.Errorf("skills: id, user_id, and agent_id are required")
	}
	if in.RestoredBy == "" {
		return ReflectSkillRestoreResult{}, ErrSkillRestoreBadCaller
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return ReflectSkillRestoreResult{}, fmt.Errorf("skills: begin reflect restore: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)

	beforeRow, err := qtx.GetSkillForUpdate(ctx, in.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ReflectSkillRestoreResult{}, ErrSkillNotRestorable
	}
	if err != nil {
		return ReflectSkillRestoreResult{}, fmt.Errorf("skills: lock reflect restore skill: %w", err)
	}
	before := mapRow(beforeRow)
	if before.Scope != "user_agent" || before.UserID != in.UserID || before.AgentID != in.AgentID || !IsReflectOwned(before) {
		return ReflectSkillRestoreResult{}, ErrSkillNotReflectOwned
	}
	if before.Status == SkillStatusActive {
		if err := tx.Commit(ctx); err != nil {
			return ReflectSkillRestoreResult{}, fmt.Errorf("skills: commit no-op reflect restore: %w", err)
		}
		return ReflectSkillRestoreResult{Skill: before, Restored: false}, nil
	}
	if before.Status != SkillStatusDeprecated {
		return ReflectSkillRestoreResult{}, ErrSkillNotRestorable
	}

	deprecateLog, err := qtx.GetLatestCuratorDeprecateSkillChangelog(ctx, sqlc.GetLatestCuratorDeprecateSkillChangelogParams{
		SkillID: in.ID,
		UserID:  in.UserID,
		AgentID: in.AgentID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ReflectSkillRestoreResult{}, ErrSkillNotRestorable
	}
	if err != nil {
		return ReflectSkillRestoreResult{}, fmt.Errorf("skills: read curator deprecate changelog: %w", err)
	}
	restoreUseCount := restoreSkillUseCount(deprecateLog.Metadata)
	afterRow, err := qtx.RestoreReflectOwnedUserAgentSkill(ctx, sqlc.RestoreReflectOwnedUserAgentSkillParams{
		ID:      in.ID,
		UserID:  in.UserID,
		AgentID: in.AgentID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ReflectSkillRestoreResult{}, ErrSkillNotRestorable
	}
	if err != nil {
		return ReflectSkillRestoreResult{}, fmt.Errorf("skills: restore reflect-owned skill: %w", err)
	}
	after := mapRow(afterRow)
	if err := qtx.UpsertSkillUsageOnReflectRestore(ctx, sqlc.UpsertSkillUsageOnReflectRestoreParams{
		SkillID:  after.ID,
		UserID:   in.UserID,
		AgentID:  in.AgentID,
		UseCount: restoreUseCount,
	}); err != nil {
		return ReflectSkillRestoreResult{}, fmt.Errorf("skills: restore skill usage: %w", err)
	}
	metadata := restoreSkillMetadata(in.RestoredBy, in.Reason, deprecateLog, restoreUseCount)
	if _, err := qtx.InsertSkillChangelog(ctx, sqlc.InsertSkillChangelogParams{
		SkillID:       after.ID,
		UserID:        pgtype.Text{String: in.UserID, Valid: true},
		AgentID:       pgtype.Text{String: in.AgentID, Valid: true},
		Scope:         after.Scope,
		Action:        "restore",
		VersionBefore: pgtype.Int8{Int64: before.Version, Valid: true},
		VersionAfter:  after.Version,
		Metadata:      metadata,
	}); err != nil {
		return ReflectSkillRestoreResult{}, fmt.Errorf("skills: record reflect restore changelog: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return ReflectSkillRestoreResult{}, fmt.Errorf("skills: commit reflect restore: %w", err)
	}
	return ReflectSkillRestoreResult{Skill: after, Restored: true}, nil
}

func restoreSkillUseCount(metadata json.RawMessage) int64 {
	var payload struct {
		UseCount int64 `json:"use_count"`
	}
	if err := json.Unmarshal(metadata, &payload); err != nil || payload.UseCount <= 0 {
		return 1
	}
	return payload.UseCount
}

func restoreSkillMetadata(restoredBy string, reason string, deprecated sqlc.SkillChangelog, useCount int64) json.RawMessage {
	payload := map[string]any{
		"restored_by":             restoredBy,
		"deprecated_changelog_id": deprecated.ID,
		"deprecated_at":           deprecated.CreatedAt.UTC().Format(time.RFC3339),
		"curator_rule":            "",
		"restored_use_count":      useCount,
	}
	if reason != "" {
		payload["reason"] = reason
	}
	deprecateMetadata := map[string]any{}
	if err := json.Unmarshal(deprecated.Metadata, &deprecateMetadata); err == nil {
		if rule, _ := deprecateMetadata["rule"].(string); rule != "" {
			payload["curator_rule"] = rule
		}
		if lastUsed, _ := deprecateMetadata["last_used_at"].(string); lastUsed != "" {
			payload["last_used_at"] = lastUsed
		}
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}

func validateReflectSkillName(name string) error {
	const maxSkillNameLength = 64
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("skills: name is required")
	}
	if len(name) > maxSkillNameLength {
		return fmt.Errorf("skills: name %q exceeds %d characters", name, maxSkillNameLength)
	}
	if !validReflectSkillNameRe.MatchString(name) || strings.HasPrefix(name, "-") || strings.HasSuffix(name, "-") || strings.Contains(name, "--") {
		return fmt.Errorf("skills: invalid skill name %q", name)
	}
	return nil
}
