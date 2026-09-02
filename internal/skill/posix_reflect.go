package skill

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/internal/platform/home"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func (s *POSIXStore) reflectCreateRetry(ctx context.Context, in ReflectSkillCreate, metadata json.RawMessage) (*managedSnapshot, error) {
	row, err := s.q.GetUserAgentSkillByName(ctx, sqlc.GetUserAgentSkillByNameParams{
		UserID: pgtype.Text{String: in.UserID, Valid: true}, AgentID: pgtype.Text{String: in.AgentID, Valid: true}, Name: in.Name,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	snapshot, err := s.loadIdentity(ctx, identityFromRow(row))
	if err != nil {
		return nil, err
	}
	if snapshot.Skill.Description != in.Description || snapshot.Skill.Status != SkillStatusActive || snapshot.Skill.DisableModelInvocation || !IsReflectOwned(snapshot.Skill) ||
		len(snapshot.Files) != 1 || snapshot.Files[0].Path != MainFile || string(snapshot.Files[0].Content) != in.MainFileContent {
		return nil, ErrSkillDigestConflict
	}
	equal, err := semanticJSONEqual(snapshot.Skill.Metadata, metadata)
	if err != nil || !equal {
		return nil, errors.Join(ErrSkillDigestConflict, err)
	}
	return &snapshot, nil
}

func insertReflectEvidence(ctx context.Context, q *sqlc.Queries, before *Skill, after Skill, action string, metadata json.RawMessage) error {
	usage := sqlc.RefreshSkillUsageOnReflectPatchParams{
		SkillID: after.ID, UserID: after.UserID, AgentID: after.AgentID, ContentDigest: digestText(after.ContentDigest),
	}
	var err error
	if action == "create" {
		err = q.UpsertSkillUsageOnReflectCreate(ctx, sqlc.UpsertSkillUsageOnReflectCreateParams{
			SkillID: usage.SkillID, UserID: usage.UserID, AgentID: usage.AgentID, ContentDigest: usage.ContentDigest,
		})
	} else {
		err = q.RefreshSkillUsageOnReflectPatch(ctx, usage)
	}
	if err != nil {
		return err
	}
	params := sqlc.InsertSkillChangelogParams{
		SkillID: after.ID, UserID: pgtype.Text{String: after.UserID, Valid: true}, AgentID: pgtype.Text{String: after.AgentID, Valid: true},
		Scope: after.Scope, Action: action, VersionAfter: after.Version, ContentDigest: digestText(after.ContentDigest), Metadata: metadata,
	}
	if before != nil {
		params.VersionBefore = pgtype.Int8{Int64: before.Version, Valid: true}
	}
	_, err = q.InsertSkillChangelog(ctx, params)
	return err
}

func (s *POSIXStore) verifyReflectEvidence(ctx context.Context, after Skill, action string) error {
	var usageDigest string
	if err := s.db.QueryRow(ctx, `SELECT content_digest FROM skill_usage WHERE skill_id=$1 AND user_id=$2 AND agent_id=$3`, after.ID, after.UserID, after.AgentID).Scan(&usageDigest); err != nil {
		return err
	}
	var exists bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM skill_changelog WHERE skill_id=$1 AND action=$2 AND content_digest=$3)`, after.ID, action, after.ContentDigest).Scan(&exists); err != nil {
		return err
	}
	if usageDigest != after.ContentDigest || !exists {
		return ErrSkillUsageChanged
	}
	return nil
}

func (s *POSIXStore) ensureReflectEvidence(ctx context.Context, before *Skill, after Skill, action string, metadata json.RawMessage) error {
	if err := s.verifyReflectEvidence(ctx, after, action); err == nil {
		return nil
	}
	var exists bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM skill_changelog WHERE skill_id=$1 AND action=$2 AND content_digest=$3)`, after.ID, action, after.ContentDigest).Scan(&exists); err != nil {
		return err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.q.WithTx(tx)
	usage := sqlc.RefreshSkillUsageOnReflectPatchParams{SkillID: after.ID, UserID: after.UserID, AgentID: after.AgentID, ContentDigest: digestText(after.ContentDigest)}
	if action == "create" {
		err = q.UpsertSkillUsageOnReflectCreate(ctx, sqlc.UpsertSkillUsageOnReflectCreateParams{
			SkillID: usage.SkillID, UserID: usage.UserID, AgentID: usage.AgentID, ContentDigest: usage.ContentDigest,
		})
	} else {
		err = q.RefreshSkillUsageOnReflectPatch(ctx, usage)
	}
	if err == nil && !exists {
		err = insertReflectEvidence(ctx, q, before, after, action, metadata)
		// insertReflectEvidence also upserts usage, which is harmless and keeps
		// this reconciliation path compact and exact-digest keyed.
	}
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *POSIXStore) CreateReflectOwnedUserAgentSkill(ctx context.Context, in ReflectSkillCreate) (skill Skill, resultErr error) {
	release, err := s.lockManagedMutations(ctx)
	if err != nil {
		return Skill{}, err
	}
	defer finishManagedMutation(release, &resultErr)
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
	metadata, err := MarkReflectOwnedMetadata(in.Metadata)
	if err != nil {
		return Skill{}, err
	}
	in.Description = strings.TrimSpace(in.Description)
	if existing, err := s.reflectCreateRetry(ctx, in, metadata); err != nil {
		return Skill{}, err
	} else if existing != nil {
		if err := s.ensureReflectEvidence(ctx, nil, existing.Skill, "create", changelog); err != nil {
			return Skill{}, err
		}
		return existing.Skill, nil
	}
	now := s.now().UTC()
	desired := Skill{
		ID: uuid.NewString()[:8], Scope: "user_agent", UserID: in.UserID, AgentID: in.AgentID,
		Name: in.Name, Description: in.Description, Status: SkillStatusActive, Metadata: metadata,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	files := []revisionFile{{Path: MainFile, Mode: 0o644, Content: []byte(in.MainFileContent)}}
	published, err := s.publish(ctx, desired, files, "", true)
	if err != nil {
		return Skill{}, err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		checkCtx, cancel := freshSkillContext()
		defer cancel()
		return Skill{}, errors.Join(err, s.removeSelection(checkCtx, desired, published.Skill.ContentDigest))
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.q.WithTx(tx)
	_, err = q.CreateSkill(ctx, createIdentityParams(desired))
	if err == nil {
		err = insertReflectEvidence(ctx, q, nil, published.Skill, "create", changelog)
	}
	if err != nil {
		checkCtx, cancel := freshSkillContext()
		defer cancel()
		removeErr := s.removeSelection(checkCtx, desired, published.Skill.ContentDigest)
		return Skill{}, errors.Join(err, removeErr)
	}
	if err := tx.Commit(ctx); err != nil {
		checkCtx, cancel := freshSkillContext()
		defer cancel()
		if verifyErr := s.verifyReflectEvidence(checkCtx, published.Skill, "create"); verifyErr != nil {
			return published.Skill, fmt.Errorf("%w: commit Reflect create: %w", home.ErrOutcomeUnknown, errors.Join(err, verifyErr))
		}
	}
	return published.Skill, nil
}

func reflectPatchMatches(expected, current managedSnapshot, in ReflectSkillPatch) (bool, error) {
	want := expected.Skill
	if in.Description != nil {
		want.Description = *in.Description
	}
	if in.Status != nil {
		want.Status = *in.Status
	}
	if in.DisableModelInvocation != nil {
		want.DisableModelInvocation = *in.DisableModelInvocation
	}
	if len(in.Metadata) > 0 {
		metadata, err := MarkReflectOwnedMetadata(in.Metadata)
		if err != nil {
			return false, err
		}
		want.Metadata = metadata
	}
	want.Version++
	if !sameSkillIdentity(want, current.Skill) || want.Description != current.Skill.Description || want.Status != current.Skill.Status || want.DisableModelInvocation != current.Skill.DisableModelInvocation || want.Version != current.Skill.Version {
		return false, nil
	}
	equal, err := semanticJSONEqual(want.Metadata, current.Skill.Metadata)
	if err != nil || !equal {
		return false, err
	}
	files := expected.Files
	if in.MainFileContent != nil {
		files, err = mergeRevisionFiles(expected.Files, map[string]string{MainFile: *in.MainFileContent}, nil)
		if err != nil {
			return false, err
		}
	}
	if len(files) != len(current.Files) {
		return false, nil
	}
	for i := range files {
		if files[i].Path != current.Files[i].Path || files[i].Mode != current.Files[i].Mode || !bytesEqual(files[i].Content, current.Files[i].Content) {
			return false, nil
		}
	}
	return true, nil
}

func bytesEqual(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func (s *POSIXStore) PatchReflectOwnedUserAgentSkill(ctx context.Context, in ReflectSkillPatch) (skill Skill, resultErr error) {
	release, err := s.lockManagedMutations(ctx)
	if err != nil {
		return Skill{}, err
	}
	defer finishManagedMutation(release, &resultErr)
	identity, err := s.GetIdentity(ctx, in.ID)
	if err != nil || identity == nil {
		return Skill{}, errors.Join(err, pgx.ErrNoRows)
	}
	if identity.Scope != "user_agent" || identity.UserID != in.UserID || identity.AgentID != in.AgentID {
		return Skill{}, ErrSkillNotReflectOwned
	}
	if !validSkillDigest(in.ExpectedDigest) {
		return Skill{}, ErrSkillDigestRequired
	}
	changelog, err := normalizeReflectSkillChangelogMetadata(in.ChangelogMetadata)
	if err != nil {
		return Skill{}, err
	}
	before, err := s.loadIdentity(ctx, *identity)
	if err != nil {
		return Skill{}, err
	}
	if !IsReflectOwned(before.Skill) {
		return Skill{}, ErrSkillNotReflectOwned
	}
	if before.Skill.ContentDigest != in.ExpectedDigest {
		expected, loadErr := s.loadIdentityRevision(ctx, *identity, in.ExpectedDigest)
		applied, matchErr := reflectPatchMatches(expected, before, in)
		if loadErr != nil || matchErr != nil || !applied {
			return Skill{}, errors.Join(ErrSkillDigestConflict, loadErr, matchErr)
		}
		if err := s.ensureReflectEvidence(ctx, &expected.Skill, before.Skill, "patch", changelog); err != nil {
			return Skill{}, err
		}
		return before.Skill, nil
	}
	after := before.Skill
	if in.Description != nil {
		after.Description = *in.Description
	}
	if in.Status != nil {
		after.Status = *in.Status
	}
	if in.DisableModelInvocation != nil {
		after.DisableModelInvocation = *in.DisableModelInvocation
	}
	if len(in.Metadata) > 0 {
		after.Metadata, err = MarkReflectOwnedMetadata(in.Metadata)
		if err != nil {
			return Skill{}, err
		}
	}
	after.Version++
	after.UpdatedAt = s.now().UTC()
	upserts := map[string]string{}
	if in.MainFileContent != nil {
		upserts[MainFile] = *in.MainFileContent
	}
	files, err := mergeRevisionFiles(before.Files, upserts, nil)
	if err != nil {
		return Skill{}, err
	}
	published, err := s.publish(ctx, after, files, in.ExpectedDigest, false)
	if err != nil {
		return Skill{}, err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return published.Skill, fmt.Errorf("%w: begin Reflect evidence: %w", home.ErrOutcomeUnknown, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := insertReflectEvidence(ctx, s.q.WithTx(tx), &before.Skill, published.Skill, "patch", changelog); err != nil {
		return published.Skill, fmt.Errorf("%w: record Reflect evidence: %w", home.ErrOutcomeUnknown, err)
	}
	if err := tx.Commit(ctx); err != nil {
		checkCtx, cancel := freshSkillContext()
		defer cancel()
		if verifyErr := s.verifyReflectEvidence(checkCtx, published.Skill, "patch"); verifyErr != nil {
			return published.Skill, fmt.Errorf("%w: commit Reflect patch: %w", home.ErrOutcomeUnknown, errors.Join(err, verifyErr))
		}
	}
	return published.Skill, nil
}

func (s *POSIXStore) DeleteReflectOwnedUserAgentSkill(ctx context.Context, in ReflectSkillDelete) (skill Skill, resultErr error) {
	release, err := s.lockManagedMutations(ctx)
	if err != nil {
		return Skill{}, err
	}
	defer finishManagedMutation(release, &resultErr)
	identity, err := s.GetIdentity(ctx, in.ID)
	if err != nil || identity == nil {
		return Skill{}, errors.Join(err, pgx.ErrNoRows)
	}
	if identity.Scope != "user_agent" || identity.UserID != in.UserID || identity.AgentID != in.AgentID {
		return Skill{}, ErrSkillNotReflectOwned
	}
	if !validSkillDigest(in.ExpectedDigest) {
		return Skill{}, ErrSkillDigestRequired
	}
	if in.ExpectedUsageLastUsedAt.IsZero() {
		return Skill{}, errors.New("skills: expected usage is required")
	}
	before, err := s.loadManagedDeleteSnapshot(ctx, *identity, in.ExpectedDigest)
	if err != nil {
		return Skill{}, err
	}
	if !IsReflectOwned(before.Skill) || before.Skill.Status != SkillStatusActive {
		return Skill{}, ErrSkillDigestConflict
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Skill{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.q.WithTx(tx)
	usage, err := q.GetSkillUsageForUpdate(ctx, sqlc.GetSkillUsageForUpdateParams{SkillID: in.ID, UserID: in.UserID, AgentID: in.AgentID})
	if err != nil || !usage.LastUsedAt.Equal(in.ExpectedUsageLastUsedAt) || usage.ContentDigest.String != in.ExpectedDigest {
		return Skill{}, errors.Join(ErrSkillUsageChanged, err)
	}
	hasActivity, err := q.HasEligiblePairActivityAfter(ctx, sqlc.HasEligiblePairActivityAfterParams{UserID: in.UserID, AgentID: in.AgentID, After: usage.LastUsedAt})
	if err != nil || !hasActivity {
		return Skill{}, errors.Join(ErrSkillUsageChanged, err)
	}
	agentID, userID := viewSQLParams(ViewContext{UserID: identity.UserID, AgentID: identity.AgentID})
	if err := q.DeleteSkill(ctx, sqlc.DeleteSkillParams{ID: identity.ID, AgentID: agentID, UserID: userID}); err != nil {
		return Skill{}, fmt.Errorf("%w: delete Reflect identity: %w", home.ErrOutcomeUnknown, err)
	}
	if err := tx.Commit(ctx); err != nil {
		checkCtx, cancel := freshSkillContext()
		defer cancel()
		if _, readErr := s.q.GetSkillByID(checkCtx, identity.ID); !errors.Is(readErr, pgx.ErrNoRows) {
			return before.Skill, fmt.Errorf("%w: commit Reflect delete: %w", home.ErrOutcomeUnknown, errors.Join(err, readErr))
		}
	}
	if err := s.cleanupDeletedSelection(before.Skill, in.ExpectedDigest); err != nil {
		return before.Skill, err
	}
	return before.Skill, nil
}

func (s *POSIXStore) TouchReflectSkillRuntimeUseDigest(ctx context.Context, id, userID, agentID, digest string) error {
	identity, err := s.GetIdentity(ctx, id)
	if err != nil || identity == nil {
		return errors.Join(err, ErrSkillUsageChanged)
	}
	if identity.Scope != "user_agent" || identity.UserID != userID || identity.AgentID != agentID || !validSkillDigest(digest) {
		return ErrSkillUsageChanged
	}
	snapshot, err := s.loadIdentity(ctx, *identity)
	if err != nil {
		return err
	}
	if snapshot.Skill.ContentDigest != digest || snapshot.Skill.Status != SkillStatusActive || snapshot.Skill.DisableModelInvocation || !IsReflectOwned(snapshot.Skill) {
		return ErrSkillUsageChanged
	}
	rows, err := s.q.TouchReflectSkillRuntimeUse(ctx, sqlc.TouchReflectSkillRuntimeUseParams{
		SkillID: id, UserID: userID, AgentID: agentID, ContentDigest: digestText(digest),
	})
	if err != nil {
		return err
	}
	if rows != 1 {
		return ErrSkillUsageChanged
	}
	return nil
}
