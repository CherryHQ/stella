-- Legacy Skill reads retained exclusively for the offline Home migration and
-- its completed-marker verifier. Home is the production current-state authority.

-- name: GetSkillByID :one
SELECT * FROM skill WHERE id = $1;

-- name: ListSkillFiles :many
SELECT * FROM skill_file WHERE skill_id = $1 ORDER BY path;

-- GetSkillMigrationFileBounds lets the offline migration reject oversized
-- source payloads before materializing any file bodies.
-- name: GetSkillMigrationFileBounds :one
SELECT COUNT(*)::bigint AS file_count,
       COALESCE(MAX(octet_length(content)), 0)::bigint AS max_content_bytes,
       COALESCE(SUM(octet_length(content)), 0)::bigint AS total_content_bytes
FROM skill_file WHERE skill_id = $1;

-- ListSkillMigrationSource returns immutable legacy rows in offline-migration
-- order. Owner IDs are validated before they become typed Home keys.
-- name: ListSkillMigrationSource :many
SELECT * FROM skill
ORDER BY scope, COALESCE(user_id::text, ''), COALESCE(agent_id, ''), name, id;

-- name: CountSkillMigrationSource :one
SELECT COUNT(*)::bigint FROM skill;

-- LockSkillMigrationSource serializes fresh-empty authority initialization with
-- legacy writers. It is held only for the marker transaction.
-- name: LockSkillMigrationSource :exec
LOCK TABLE skill IN SHARE MODE;

-- name: ListSkillMigrationChangelog :many
SELECT * FROM skill_changelog
WHERE skill_id = $1
ORDER BY version_after, created_at, id;

-- GetSkillMigrationChangelogBounds rejects oversized archive provenance before
-- materializing variable text or JSON bodies.
-- name: GetSkillMigrationChangelogBounds :one
SELECT COUNT(*)::bigint AS changelog_count,
       COALESCE(MAX(octet_length(id::text) + octet_length(skill_id) + octet_length(COALESCE(user_id::text, '')) + octet_length(COALESCE(agent_id, '')) + octet_length(scope) + octet_length(action) + octet_length(metadata::text)), 0)::bigint AS max_content_bytes,
       COALESCE(SUM(octet_length(id::text) + octet_length(skill_id) + octet_length(COALESCE(user_id::text, '')) + octet_length(COALESCE(agent_id, '')) + octet_length(scope) + octet_length(action) + octet_length(metadata::text)), 0)::bigint AS total_content_bytes
FROM skill_changelog WHERE skill_id = $1;
