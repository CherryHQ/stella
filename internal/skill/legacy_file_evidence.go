package skill

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/internal/platform/home"
	"github.com/CherryHQ/stella/internal/plugin"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

const legacyFileEvidenceMigration = "filesystem_resources_v1"

// FinalizeLegacyFileEvidence records the durable Skill identity bridge after
// publication. The filesystem is re-read through a fresh capability and the
// source rows are locked before any evidence is changed. A source that no
// longer proves Reflect ownership is still linked for history, but never gets
// a new Reflect authorization.
func FinalizeLegacyFileEvidence(ctx context.Context, tx pgx.Tx, roots home.RootOpener, prepared PreparedLegacyFileExport) error {
	if tx == nil || roots == nil {
		return errors.New("skills: transaction and resource roots are required")
	}
	q := sqlc.New(tx)
	for _, entry := range prepared.Skills {
		if err := validateLegacyEntry(entry); err != nil {
			return err
		}
		if entry.SourceIdentityID == "" || !validSkillDigest(entry.SourceDigest) || entry.ContentDigest == "" {
			return fmt.Errorf("skills: invalid evidence for legacy Skill %q", entry.Name)
		}
		if err := verifyPublishedLegacySkill(ctx, roots, entry); err != nil {
			return err
		}
		if err := finalizeLegacySkillEvidence(ctx, tx, q, entry); err != nil {
			return err
		}
	}
	return nil
}

func verifyPublishedLegacySkill(ctx context.Context, roots home.RootOpener, entry LegacyFileSkillExport) error {
	root, err := roots.OpenRoot(ctx, home.WorkspaceRequest{UserID: entry.UserID, AgentID: entry.AgentID}, legacyResourceRoot(entry.Scope), home.RootReadOnly)
	if err != nil {
		return fmt.Errorf("verify published Skill %s: %w", entry.Name, err)
	}
	defer func() { _ = root.Close() }()
	base := path.Join("skills", entry.Name)
	if err := compareLegacyTree(ctx, root, base, entry.Files); err != nil {
		return fmt.Errorf("verify published Skill %s bytes: %w", entry.Name, err)
	}
	content, err := plugin.CaptureResource(ctx, root, base)
	if err != nil {
		return fmt.Errorf("capture published Skill %s: %w", entry.Name, err)
	}
	if content.Digest != entry.ContentDigest {
		return fmt.Errorf("published Skill %s digest changed: got %q, want %q", entry.Name, content.Digest, entry.ContentDigest)
	}
	return nil
}

type legacySkillUsageEvidence struct {
	resourceID pgtype.Text
	userID     string
	agentID    string
	digest     pgtype.Text
}

func finalizeLegacySkillEvidence(ctx context.Context, tx pgx.Tx, q *sqlc.Queries, entry LegacyFileSkillExport) error {
	if err := lockLegacySkillIdentity(ctx, tx, entry); err != nil {
		return err
	}
	usage, usagePresent, err := lockLegacySkillUsage(ctx, tx, entry)
	if err != nil {
		return err
	}
	if usagePresent && usage.resourceID.Valid && usage.resourceID.String != entry.FileSkillID {
		return fmt.Errorf("skills: legacy Skill %s is linked to another resource", entry.SourceIdentityID)
	}
	currentFrontmatter, err := parseFrontmatter(string(entry.Files[MainFile].Content))
	if err != nil {
		return fmt.Errorf("skills: legacy Skill %s target frontmatter: %w", entry.SourceIdentityID, err)
	}

	latest, latestErr := latestLegacySkillChangelog(ctx, tx, entry)
	if latestErr != nil && !errors.Is(latestErr, pgx.ErrNoRows) {
		return latestErr
	}
	alreadyAudited, err := hasLegacyFileEvidenceAudit(ctx, tx, entry)
	if err != nil {
		return err
	}
	linked, err := q.LinkLegacySkillResource(ctx, sqlc.LinkLegacySkillResourceParams{
		LegacySkillID: entry.SourceIdentityID,
		Scope:         entry.Scope,
		UserID:        pgtype.Text{String: entry.UserID, Valid: entry.UserID != ""},
		AgentID:       pgtype.Text{String: entry.AgentID, Valid: entry.AgentID != ""},
		Name:          entry.Name,
		ResourceID:    entry.FileSkillID,
	})
	if err != nil {
		return err
	}
	if !linked.LegacyMatch {
		return fmt.Errorf("skills: legacy Skill %s identity disappeared", entry.SourceIdentityID)
	}
	if linked.HasConflict {
		return fmt.Errorf("skills: legacy Skill %s resource identity conflict", entry.SourceIdentityID)
	}
	if alreadyAudited {
		// The audit and digest swap are committed atomically. A later manual or
		// revoked row must remain the latest state and must never be revived.
		return nil
	}

	oldDigest := usage.digest.String
	trusted := IsReflectOwned(Skill{ID: entry.SourceIdentityID, Scope: entry.Scope, UserID: entry.UserID, AgentID: entry.AgentID, Name: entry.Name, Metadata: entry.Metadata}) &&
		entry.Scope == "user_agent" && usagePresent && usage.digest.Valid && oldDigest == entry.SourceDigest &&
		usage.userID == entry.UserID && usage.agentID == entry.AgentID &&
		currentFrontmatter.Status == SkillStatusActive && !entry.DisableModelInvocation &&
		latestErr == nil && latest.Action != "delete" && latest.ContentDigest == entry.SourceDigest &&
		latest.Scope == entry.Scope && latest.UserID == entry.UserID && latest.AgentID == entry.AgentID
	if !trusted {
		return nil
	}

	newDigest := strings.TrimPrefix(entry.ContentDigest, "sha256:")
	if !validSkillDigest(newDigest) {
		return fmt.Errorf("skills: invalid published digest for %s", entry.Name)
	}
	result, err := tx.Exec(ctx, `
UPDATE skill_usage
SET content_digest = $1
WHERE skill_id = $2
  AND resource_id = $3
  AND user_id = $4::uuid
  AND agent_id = $5::text
`, newDigest, entry.SourceIdentityID, entry.FileSkillID, entry.UserID, entry.AgentID)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return fmt.Errorf("skills: legacy Skill %s usage disappeared during evidence finalization", entry.SourceIdentityID)
	}
	auditMetadata, err := json.Marshal(map[string]string{
		"migration":     legacyFileEvidenceMigration,
		"migrated_from": entry.SourceIdentityID,
		"source_digest": entry.SourceDigest,
		"target_digest": newDigest,
	})
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
INSERT INTO skill_changelog
	  (skill_id, resource_id, user_id, agent_id, scope, action, version_after, content_digest, writer, metadata)
SELECT $1, $2, user_id, agent_id, scope, 'patch', version, $3, 'reflect', $4::jsonb
FROM skill
WHERE id = $1
`, entry.SourceIdentityID, entry.FileSkillID, newDigest, auditMetadata)
	if err != nil {
		return err
	}
	return nil
}

func lockLegacySkillIdentity(ctx context.Context, tx pgx.Tx, entry LegacyFileSkillExport) error {
	var id, scope, name string
	var userID, agentID pgtype.Text
	err := tx.QueryRow(ctx, `
SELECT id, scope, user_id::text, agent_id::text, name
FROM skill
WHERE id = $1
FOR UPDATE
`, entry.SourceIdentityID).Scan(&id, &scope, &userID, &agentID, &name)
	if err != nil {
		return err
	}
	if id != entry.SourceIdentityID || scope != entry.Scope || userID.String != entry.UserID || agentID.String != entry.AgentID || name != entry.Name {
		return fmt.Errorf("skills: legacy Skill %s identity changed", entry.SourceIdentityID)
	}
	return nil
}

func lockLegacySkillUsage(ctx context.Context, tx pgx.Tx, entry LegacyFileSkillExport) (legacySkillUsageEvidence, bool, error) {
	rows, err := tx.Query(ctx, `
SELECT skill_id, resource_id, user_id::text, agent_id::text, use_count, last_used_at, content_digest
FROM skill_usage
WHERE skill_id = $1 OR resource_id = $2
	FOR UPDATE
`, entry.SourceIdentityID, entry.FileSkillID)
	if err != nil {
		return legacySkillUsageEvidence{}, false, err
	}
	defer rows.Close()
	var result legacySkillUsageEvidence
	var skillID string
	var useCount int64
	var lastUsedAt time.Time
	count := 0
	for rows.Next() {
		count++
		if count > 1 {
			return legacySkillUsageEvidence{}, false, fmt.Errorf("skills: legacy Skill %s has split usage evidence", entry.SourceIdentityID)
		}
		if err := rows.Scan(&skillID, &result.resourceID, &result.userID, &result.agentID, &useCount, &lastUsedAt, &result.digest); err != nil {
			return legacySkillUsageEvidence{}, false, err
		}
	}
	if err := rows.Err(); err != nil {
		return legacySkillUsageEvidence{}, false, err
	}
	return result, count == 1, nil
}

func latestLegacySkillChangelog(ctx context.Context, tx pgx.Tx, entry LegacyFileSkillExport) (SkillChangelog, error) {
	var row sqlc.SkillChangelog
	err := tx.QueryRow(ctx, `
SELECT id, skill_id, user_id, agent_id, scope, action, version_before, version_after, metadata, created_at, content_digest, writer, resource_id
FROM skill_changelog
WHERE skill_id = $1 OR resource_id = $2
ORDER BY created_at DESC, id DESC
LIMIT 1
`, entry.SourceIdentityID, entry.FileSkillID).Scan(
		&row.ID, &row.SkillID, &row.UserID, &row.AgentID, &row.Scope, &row.Action, &row.VersionBefore, &row.VersionAfter,
		&row.Metadata, &row.CreatedAt, &row.ContentDigest, &row.Writer, &row.ResourceID,
	)
	if err != nil {
		return SkillChangelog{}, err
	}
	return mapChangelogRow(row), nil
}

func hasLegacyFileEvidenceAudit(ctx context.Context, tx pgx.Tx, entry LegacyFileSkillExport) (bool, error) {
	var exists bool
	err := tx.QueryRow(ctx, `
SELECT EXISTS (
  SELECT 1
  FROM skill_changelog
  WHERE skill_id = $1
    AND resource_id = $2
    AND writer = 'reflect'
    AND action = 'patch'
    AND metadata->>'migration' = $3
)
`, entry.SourceIdentityID, entry.FileSkillID, legacyFileEvidenceMigration).Scan(&exists)
	return exists, err
}
