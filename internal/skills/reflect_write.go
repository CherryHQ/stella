package skills

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

var validReflectSkillNameRe = regexp.MustCompile(`^[a-z0-9-]+$`)

var (
	ErrSkillVersionConflict = errors.New("skill version conflict")
	ErrSkillNotReflectOwned = errors.New("skill is not reflect-owned")
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
		UserID:                 pgtype.Text{String: in.UserID, Valid: true},
		AgentID:                pgtype.Text{String: in.AgentID, Valid: true},
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
