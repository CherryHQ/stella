-- +goose Up
SET LOCAL lock_timeout = '5s';

CREATE TABLE "ctx_group_fact" (
  "id" uuid NOT NULL DEFAULT uuidv7(),
  "group_id" uuid NOT NULL,
  "subject" text NOT NULL,
  "subject_id" text NULL,
  "content" text NOT NULL,
  "status" text NOT NULL,
  "source" text NOT NULL,
  "created_at" timestamptz NOT NULL DEFAULT now(),
  "updated_at" timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY ("id"),
  CONSTRAINT "ctx_group_fact_group_id_fkey"
    FOREIGN KEY ("group_id")
    REFERENCES "ctx_group_state" ("id")
    ON UPDATE NO ACTION
    ON DELETE CASCADE,
  CONSTRAINT "ctx_group_fact_content_nonempty"
    CHECK (btrim("content") <> ''),
  CONSTRAINT "ctx_group_fact_subject_allowed"
    CHECK ("subject" IN ('group', 'human', 'agent')),
  CONSTRAINT "ctx_group_fact_subject_shape"
    CHECK (
      ("subject" = 'group' AND "subject_id" IS NULL)
      OR
      ("subject" <> 'group' AND "subject_id" IS NOT NULL AND btrim("subject_id") <> '')
    ),
  CONSTRAINT "ctx_group_fact_status_allowed"
    CHECK ("status" IN ('active', 'deprecated')),
  CONSTRAINT "ctx_group_fact_source_allowed"
    CHECK ("source" = 'reflect')
);

CREATE INDEX "idx_ctx_group_fact_active_subject"
ON "ctx_group_fact" ("group_id", "status", "subject", "subject_id", "created_at", "id");

CREATE TABLE "ctx_group_fact_changelog" (
  "id" uuid NOT NULL DEFAULT uuidv7(),
  "group_id" uuid NOT NULL,
  "fact_id" uuid NOT NULL,
  "action" text NOT NULL,
  "source" text NOT NULL,
  "group_version_before" bigint NOT NULL,
  "group_version_after" bigint NOT NULL,
  "before_state" jsonb NULL,
  "after_state" jsonb NULL,
  "created_at" timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY ("id"),
  CONSTRAINT "ctx_group_fact_changelog_group_id_fkey"
    FOREIGN KEY ("group_id")
    REFERENCES "ctx_group_state" ("id")
    ON UPDATE NO ACTION
    ON DELETE CASCADE,
  CONSTRAINT "ctx_group_fact_changelog_fact_id_fkey"
    FOREIGN KEY ("fact_id")
    REFERENCES "ctx_group_fact" ("id")
    ON UPDATE NO ACTION
    ON DELETE CASCADE,
  CONSTRAINT "ctx_group_fact_changelog_action_allowed"
    CHECK ("action" IN ('create', 'replace', 'deprecate')),
  CONSTRAINT "ctx_group_fact_changelog_source_allowed"
    CHECK ("source" = 'reflect'),
  CONSTRAINT "ctx_group_fact_changelog_version_order"
    CHECK ("group_version_after" > "group_version_before")
);

CREATE INDEX "idx_ctx_group_fact_changelog_group_version"
ON "ctx_group_fact_changelog" ("group_id", "group_version_after", "created_at", "id");

-- +goose Down
SET LOCAL lock_timeout = '5s';

DROP TABLE "ctx_group_fact_changelog";
DROP TABLE "ctx_group_fact";
