-- +goose Up
-- skill_id remains the compatibility primary key. Home-authoritative Skills use
-- their canonical filesystem ID here until a later, explicit rename/drop.
ALTER TABLE skill_usage
  DROP CONSTRAINT skill_usage_skill_id_fkey,
  ADD CONSTRAINT skill_usage_logical_fields_check CHECK (
    (scope IS NULL AND name IS NULL AND last_content_digest IS NULL)
    OR
    (
      scope = 'user_agent'
      AND name IS NOT NULL
      AND char_length(name) <= 64
      AND name ~ '^[a-z0-9]+(-[a-z0-9]+)*$'
      AND last_content_digest IS NOT NULL
      AND last_content_digest ~ '^[0-9a-f]{64}$'
    )
  );

-- +goose Down
ALTER TABLE skill_usage
  DROP CONSTRAINT skill_usage_logical_fields_check;

-- Do not validate this restored compatibility FK: logical Home IDs intentionally
-- have no skill row, so validating existing telemetry would make Down unsafe.
-- NOT VALID still protects any new legacy-shaped writes after a local rollback.
ALTER TABLE skill_usage
  ADD CONSTRAINT skill_usage_skill_id_fkey FOREIGN KEY (skill_id)
    REFERENCES skill (id) ON UPDATE NO ACTION ON DELETE CASCADE NOT VALID;
