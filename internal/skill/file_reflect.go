package skill

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/internal/platform/home"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func (s *FileStore) reflectEvidence(ctx context.Context, id string) (SkillChangelog, error) {
	row, err := s.q.GetLatestSkillChangelogBySkill(ctx, id)
	if err != nil {
		return SkillChangelog{}, err
	}
	return mapChangelogRow(row), nil
}

func eligibleFileReflectEvidence(latest SkillChangelog, sk Skill) bool {
	return latest.SkillID == sk.ID && latest.Scope == sk.Scope && latest.UserID == sk.UserID && latest.AgentID == sk.AgentID &&
		latest.Writer == ReflectSkillCreatedBy && latest.Action != "delete" && latest.ContentDigest == sk.ContentDigest &&
		sk.Scope == "user_agent" && sk.Status == SkillStatusActive && !sk.DisableModelInvocation
}

func (s *FileStore) ListActiveReflectOwnedUserAgentSkills(ctx context.Context, userID, agentID string) ([]Skill, error) {
	current, err := s.captureScopeSkills(ctx, Skill{Scope: "user_agent", UserID: userID, AgentID: agentID})
	if err != nil {
		return nil, err
	}
	out := make([]Skill, 0, len(current))
	for _, skill := range current {
		latest, err := s.reflectEvidence(ctx, skill.ID)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if eligibleFileReflectEvidence(latest, skill) {
			out = append(out, skill)
		}
	}
	slices.SortFunc(out, func(a, b Skill) int {
		return strings.Compare(a.ID, b.ID)
	})
	return out, nil
}

func (s *FileStore) CreateReflectOwnedUserAgentSkill(ctx context.Context, in ReflectSkillCreate) (Skill, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireMutation(); err != nil {
		return Skill{}, err
	}
	if err := validateReflectSkillName(in.Name); err != nil {
		return Skill{}, err
	}
	if in.UserID == "" || in.AgentID == "" || in.MainFileContent == "" {
		return Skill{}, errors.New("skills: user_id, agent_id, and SKILL.md are required")
	}
	changelog, err := normalizeReflectSkillChangelogMetadata(in.ChangelogMetadata)
	if err != nil {
		return Skill{}, err
	}
	metadata, err := sanitizeMetadata(in.Metadata)
	if err != nil {
		return Skill{}, err
	}
	name := strings.TrimSpace(in.Name)
	desired := Skill{ID: fileSkillID("user_agent", in.UserID, in.AgentID, name), Scope: "user_agent", UserID: in.UserID, AgentID: in.AgentID, Name: name, Description: strings.TrimSpace(in.Description), Status: SkillStatusActive, Metadata: metadata, Version: 0}
	prepared, err := prepareSkillFiles(desired, map[string]ManagedSkillFile{
		MainFile: {Content: []byte(in.MainFileContent), Mode: 0o644},
	}, nil)
	if err != nil {
		return Skill{}, err
	}
	if current, currentErr := s.LoadCurrentRevision(ctx, desired); currentErr == nil {
		latest, evidenceErr := s.reflectEvidence(ctx, desired.ID)
		if evidenceErr != nil && !errors.Is(evidenceErr, pgx.ErrNoRows) {
			return Skill{}, evidenceErr
		}
		if evidenceErr == nil && eligibleFileReflectEvidence(latest, current.Skill) && revisionMatchesFiles(current, prepared) {
			return current.Skill, nil
		}
		return Skill{}, ErrSkillDigestConflict
	} else if !errors.Is(currentErr, pgx.ErrNoRows) {
		return Skill{}, currentErr
	}
	created, err := s.createFileSkill(ctx, desired, map[string]ManagedSkillFile{MainFile: {Content: []byte(in.MainFileContent), Mode: 0o644}})
	if err != nil {
		return Skill{}, err
	}
	if err := s.recordFileSkillChange(ctx, nil, created.Skill, "create", ReflectSkillCreatedBy, changelog); err != nil {
		return created.Skill, fmt.Errorf("%w: record Reflect create: %w", home.ErrOutcomeUnknown, err)
	}
	return created.Skill, nil
}

func (s *FileStore) PatchReflectOwnedUserAgentSkill(ctx context.Context, in ReflectSkillPatch) (Skill, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireMutation(); err != nil {
		return Skill{}, err
	}
	identity, ok := decodeFileSkillID(in.ID)
	if !ok || identity.Scope != "user_agent" || identity.UserID != in.UserID || identity.AgentID != in.AgentID || !validSkillDigest(in.ExpectedDigest) {
		return Skill{}, ErrSkillNotReflectOwned
	}
	before, err := s.LoadCurrentRevision(ctx, identity)
	if err != nil {
		return Skill{}, err
	}
	latest, err := s.reflectEvidence(ctx, in.ID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Skill{}, err
	}
	if before.Skill.ContentDigest != in.ExpectedDigest {
		return Skill{}, ErrSkillDigestConflict
	}
	if errors.Is(err, pgx.ErrNoRows) || !eligibleFileReflectEvidence(latest, before.Skill) {
		return Skill{}, ErrSkillNotReflectOwned
	}
	changelog, err := normalizeReflectSkillChangelogMetadata(in.ChangelogMetadata)
	if err != nil {
		return Skill{}, err
	}
	patch := ManagedSkillUpdate{ID: in.ID, UserID: in.UserID, AgentID: in.AgentID, Scope: "user_agent", ExpectedDigest: in.ExpectedDigest, Files: map[string]string{}, Patch: UpdatePatch{Description: in.Description, Status: in.Status, DisableModelInvocation: in.DisableModelInvocation}}
	if len(in.Metadata) > 0 {
		patch.Patch.Metadata, err = sanitizeMetadata(in.Metadata)
		if err != nil {
			return Skill{}, err
		}
	}
	if in.MainFileContent != nil {
		patch.Files[MainFile] = *in.MainFileContent
	}
	updatedBefore, updated, err := s.updateFileSkill(ctx, patch)
	if err != nil {
		return Skill{}, err
	}
	if err := s.recordFileSkillChange(ctx, &updatedBefore.Skill, updated.Skill, "patch", ReflectSkillCreatedBy, changelog); err != nil {
		return updated.Skill, fmt.Errorf("%w: record Reflect patch: %w", home.ErrOutcomeUnknown, err)
	}
	return updated.Skill, nil
}

func (s *FileStore) DeleteReflectOwnedUserAgentSkill(ctx context.Context, in ReflectSkillDelete) (Skill, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireMutation(); err != nil {
		return Skill{}, err
	}
	if !validSkillDigest(in.ExpectedDigest) || in.UserID == "" || in.AgentID == "" || in.ExpectedUsageLastUsedAt.IsZero() {
		return Skill{}, ErrSkillUsageChanged
	}
	identity, ok := decodeFileSkillID(in.ID)
	if !ok || identity.Scope != "user_agent" || identity.UserID != in.UserID || identity.AgentID != in.AgentID {
		return Skill{}, ErrSkillNotReflectOwned
	}
	before, err := s.LoadCurrentRevision(ctx, identity)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, fs.ErrNotExist) {
			return Skill{}, ErrSkillUsageChanged
		}
		return Skill{}, err
	}
	latest, err := s.reflectEvidence(ctx, in.ID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Skill{}, err
	}
	if before.Skill.ContentDigest != in.ExpectedDigest {
		return Skill{}, ErrSkillDigestConflict
	}
	if errors.Is(err, pgx.ErrNoRows) || !eligibleFileReflectEvidence(latest, before.Skill) {
		return Skill{}, ErrSkillUsageChanged
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Skill{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.q.WithTx(tx)
	if err := verifyReflectDeleteUsageQ(ctx, q, in); err != nil {
		return Skill{}, err
	}
	deleted, err := s.deleteFileSkill(ctx, ManagedSkillDelete{ID: in.ID, UserID: in.UserID, AgentID: in.AgentID, Scope: "user_agent", ExpectedDigest: in.ExpectedDigest})
	if err != nil {
		return Skill{}, err
	}
	if err := recordFileSkillChangeQ(ctx, q, &deleted.Skill, deleted.Skill, "delete", ReflectSkillCreatedBy, json.RawMessage(`{}`)); err != nil {
		return deleted.Skill, fmt.Errorf("%w: record Reflect delete: %w", home.ErrOutcomeUnknown, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return deleted.Skill, fmt.Errorf("%w: commit Reflect delete: %w", home.ErrOutcomeUnknown, err)
	}
	return deleted.Skill, nil
}

func verifyReflectDeleteUsageQ(ctx context.Context, q *sqlc.Queries, in ReflectSkillDelete) error {
	usage, err := q.GetSkillUsageForUpdate(ctx, sqlc.GetSkillUsageForUpdateParams{SkillID: in.ID, UserID: in.UserID, AgentID: in.AgentID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrSkillUsageChanged
		}
		return err
	}
	if !usage.LastUsedAt.Equal(in.ExpectedUsageLastUsedAt) || usage.ContentDigest.String != in.ExpectedDigest {
		return ErrSkillUsageChanged
	}
	active, err := q.HasEligiblePairActivityAfter(ctx, sqlc.HasEligiblePairActivityAfterParams{UserID: in.UserID, AgentID: in.AgentID, After: usage.LastUsedAt})
	if err != nil {
		return err
	}
	if !active {
		return ErrSkillUsageChanged
	}
	return nil
}

func (s *FileStore) TouchReflectSkillRuntimeUseDigest(ctx context.Context, id, userID, agentID, digest string) error {
	if s == nil || s.q == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !isFileSkillID(id) || !validSkillDigest(digest) {
		return nil
	}
	latest, err := s.reflectEvidence(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if latest.Writer != ReflectSkillCreatedBy || latest.Action == "delete" || latest.UserID != userID || latest.AgentID != agentID || latest.ContentDigest != digest {
		return nil
	}
	_, err = s.q.TouchReflectSkillRuntimeUse(ctx, sqlc.TouchReflectSkillRuntimeUseParams{SkillID: id, UserID: userID, AgentID: agentID, ContentDigest: pgtype.Text{String: digest, Valid: true}})
	return err
}

func (s *FileStore) ListSkillChangelogBySkill(ctx context.Context, skillID string, limit int) ([]SkillChangelog, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.q.ListSkillChangelogBySkill(ctx, sqlc.ListSkillChangelogBySkillParams{SkillID: skillID, LimitCount: int32(limit)})
	if err != nil {
		return nil, err
	}
	out := make([]SkillChangelog, 0, len(rows))
	for _, row := range rows {
		out = append(out, mapChangelogRow(row))
	}
	return out, nil
}

func (s *FileStore) recordFileSkillChange(ctx context.Context, before *Skill, after Skill, action, writer string, metadata json.RawMessage) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.q.WithTx(tx)
	if err := recordFileSkillChangeQ(ctx, q, before, after, action, writer, metadata); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func recordFileSkillChangeQ(ctx context.Context, q *sqlc.Queries, before *Skill, after Skill, action, writer string, metadata json.RawMessage) error {
	args := sqlc.RefreshSkillUsageOnReflectPatchParams{SkillID: after.ID, UserID: after.UserID, AgentID: after.AgentID, ContentDigest: pgtype.Text{String: after.ContentDigest, Valid: validSkillDigest(after.ContentDigest)}}
	if writer == ReflectSkillCreatedBy && after.Scope == "user_agent" {
		switch action {
		case "create":
			if err := q.UpsertSkillUsageOnReflectCreate(ctx, sqlc.UpsertSkillUsageOnReflectCreateParams(args)); err != nil {
				return err
			}
		case "patch":
			if err := q.RefreshSkillUsageOnReflectPatch(ctx, args); err != nil {
				return err
			}
		}
	}
	params := sqlc.InsertSkillChangelogParams{SkillID: after.ID, UserID: pgtype.Text{String: after.UserID, Valid: after.UserID != ""}, AgentID: pgtype.Text{String: after.AgentID, Valid: after.AgentID != ""}, Scope: after.Scope, Action: action, VersionAfter: after.Version, ContentDigest: args.ContentDigest, Writer: writer, Metadata: metadata}
	if before != nil {
		params.VersionBefore = pgtype.Int8{Int64: before.Version, Valid: true}
	}
	if _, err := q.InsertSkillChangelog(ctx, params); err != nil {
		return err
	}
	return nil
}
