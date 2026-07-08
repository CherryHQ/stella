-- +goose Up
ALTER TABLE "skill"
  ADD COLUMN "version" bigint NOT NULL DEFAULT 1;

CREATE TABLE "skill_changelog" (
  "id" uuid NOT NULL DEFAULT uuidv7(),
  "skill_id" text NOT NULL,
  "user_id" uuid NULL,
  "agent_id" text NULL,
  "scope" text NOT NULL,
  "action" text NOT NULL,
  "version_before" bigint NULL,
  "version_after" bigint NOT NULL,
  "metadata" jsonb NOT NULL DEFAULT '{}',
  "created_at" timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY ("id"),
  CONSTRAINT "skill_changelog_action_check" CHECK ("action" IN ('create', 'patch')),
  CONSTRAINT "skill_changelog_skill_id_fkey" FOREIGN KEY ("skill_id") REFERENCES "skill" ("id") ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT "skill_changelog_user_id_fkey" FOREIGN KEY ("user_id") REFERENCES "auth_user" ("id") ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT "skill_changelog_agent_id_fkey" FOREIGN KEY ("agent_id") REFERENCES "agent" ("id") ON UPDATE NO ACTION ON DELETE CASCADE
);

CREATE INDEX "idx_skill_changelog_skill_version" ON "skill_changelog" ("skill_id", "version_after" DESC);
CREATE INDEX "idx_skill_changelog_owner" ON "skill_changelog" ("user_id", "agent_id", "scope", "created_at" DESC);

-- +goose Down
DROP TABLE "skill_changelog";

ALTER TABLE "skill"
  DROP COLUMN "version";
