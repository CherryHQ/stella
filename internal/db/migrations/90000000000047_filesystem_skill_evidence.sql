-- +goose Up
-- File-backed Skills no longer require a durable identity row. Keep usage and
-- changelog rows as business evidence even after their logical file is gone.
SET LOCAL lock_timeout = '5s';
ALTER TABLE skill_usage
  DROP CONSTRAINT skill_usage_skill_id_fkey;

ALTER TABLE skill_changelog
  DROP CONSTRAINT skill_changelog_skill_id_fkey;

ALTER TABLE skill_changelog
  ADD COLUMN writer text NOT NULL DEFAULT '';

ALTER TABLE skill_changelog
  DROP CONSTRAINT skill_changelog_action_check,
  ADD CONSTRAINT skill_changelog_action_check CHECK (action IN ('create', 'patch', 'delete'));

-- +goose Down
-- This migration is intentionally irreversible after file-backed evidence is
-- written: restoring these foreign keys would reject valid orphan history.
SELECT 1;
