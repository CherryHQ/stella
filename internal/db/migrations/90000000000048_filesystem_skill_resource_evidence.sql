-- +goose Up
SET LOCAL lock_timeout = '5s';

-- A migrated PostgreSQL Skill keeps its historical skill_id while new
-- filesystem identity and evidence use the deterministic fileSkillID. The
-- nullable link is populated only after the exporter has verified one source
-- Skill and its published file tree.
ALTER TABLE skill_usage
  ADD COLUMN resource_id text;

ALTER TABLE skill_changelog
  ADD COLUMN resource_id text;

-- One usage row per logical Skill, whether its key is still legacy or has been
-- linked to a filesystem resource. PostgreSQL can infer this expression index
-- for ON CONFLICT ((COALESCE(resource_id, skill_id))).
CREATE UNIQUE INDEX idx_skill_usage_resource_identity
  ON skill_usage ((COALESCE(resource_id, skill_id)));

-- Historical rows and linked rows are both addressed through the logical
-- identity expression used by the read queries. Keep ordering in the index
-- for latest-evidence reads.
CREATE INDEX idx_skill_changelog_resource_lookup
  ON skill_changelog ((COALESCE(resource_id, skill_id)), created_at DESC, id DESC);

-- +goose Down
DROP INDEX idx_skill_changelog_resource_lookup;
DROP INDEX idx_skill_usage_resource_identity;
ALTER TABLE skill_changelog
  DROP COLUMN resource_id;
ALTER TABLE skill_usage
  DROP COLUMN resource_id;
