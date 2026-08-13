-- +goose Up
ALTER TABLE skill_changelog
  ADD COLUMN content_digest text NULL,
  ADD CONSTRAINT skill_changelog_content_digest_check CHECK (
    content_digest IS NULL OR content_digest ~ '^[0-9a-f]{64}$'
  );

ALTER TABLE skill_usage
  ADD COLUMN content_digest text NULL,
  ADD CONSTRAINT skill_usage_content_digest_check CHECK (
    content_digest IS NULL OR content_digest ~ '^[0-9a-f]{64}$'
  );

CREATE TABLE skill_home_migration (
  id text PRIMARY KEY,
  state text NOT NULL,
  source_skill_count bigint NOT NULL,
  source_file_count bigint NOT NULL,
  source_content_bytes bigint NOT NULL,
  source_inventory_digest text NOT NULL,
  inventory jsonb NOT NULL,
  writers_stopped_attested_at timestamptz NOT NULL,
  backup_verified_attested_at timestamptz NOT NULL,
  completed_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT skill_home_migration_state_check CHECK (state = 'completed'),
  CONSTRAINT skill_home_migration_counts_check CHECK (
    source_skill_count >= 0 AND source_file_count >= 0 AND source_content_bytes >= 0
  ),
  CONSTRAINT skill_home_migration_digest_check CHECK (
    source_inventory_digest ~ '^[0-9a-f]{64}$'
  ),
  CONSTRAINT skill_home_migration_inventory_check CHECK (jsonb_typeof(inventory) = 'array'),
  CONSTRAINT skill_home_migration_time_order_check CHECK (
    completed_at >= writers_stopped_attested_at AND
    completed_at >= backup_verified_attested_at AND
    updated_at >= created_at
  )
);

-- +goose Down
DROP TABLE skill_home_migration;

ALTER TABLE skill_usage
  DROP CONSTRAINT skill_usage_content_digest_check,
  DROP COLUMN content_digest;

ALTER TABLE skill_changelog
  DROP CONSTRAINT skill_changelog_content_digest_check,
  DROP COLUMN content_digest;
