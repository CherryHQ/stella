-- +goose Up
-- Skill availability is controlled by disable_model_invocation. Draft was a
-- second, conflicting switch, so preserve existing skills while making their
-- lifecycle state active and keeping SKILL.md frontmatter consistent.
WITH converted AS (
  UPDATE skill
  SET status = 'active', version = version + 1, updated_at = now()
  WHERE status = 'draft'
  RETURNING id
)
UPDATE skill_file
SET content = regexp_replace(
  content,
  '(^|\n)status:[ \t]*draft([ \t]*\r?\n)',
  E'\\1status: active\\2',
  'g'
)
WHERE path = 'SKILL.md'
  AND skill_id IN (SELECT id FROM converted);

ALTER TABLE skill
  ADD CONSTRAINT skill_status_check
  CHECK (status IN ('active', 'deprecated'));

-- +goose Down
ALTER TABLE skill DROP CONSTRAINT IF EXISTS skill_status_check;
-- Existing drafts were promoted to active and are intentionally not recreated.
SELECT 1;
