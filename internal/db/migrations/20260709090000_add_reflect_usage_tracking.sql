-- +goose Up
CREATE TABLE "knowledge_usage" (
  "fact_id" uuid NOT NULL,
  "user_id" uuid NOT NULL,
  "agent_id" text NOT NULL,
  "last_used_at" timestamptz NOT NULL DEFAULT now(),
  "created_at" timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY ("fact_id"),
  CONSTRAINT "knowledge_usage_fact_id_fkey" FOREIGN KEY ("fact_id") REFERENCES "facts" ("id") ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT "knowledge_usage_user_id_fkey" FOREIGN KEY ("user_id") REFERENCES "auth_user" ("id") ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT "knowledge_usage_agent_id_fkey" FOREIGN KEY ("agent_id") REFERENCES "agent" ("id") ON UPDATE NO ACTION ON DELETE CASCADE
);

CREATE INDEX "idx_knowledge_usage_last_used" ON "knowledge_usage" ("last_used_at", "fact_id");

-- Backfill usage only for records that already carry durable Reflect ownership.
-- This does not infer provenance for legacy skill-backed knowledge or skills.
INSERT INTO "knowledge_usage" ("fact_id", "user_id", "agent_id", "last_used_at", "created_at")
SELECT f."id", f."user_id", f."agent_id", now(), now()
FROM "facts" f
WHERE f."scope" = 'user_agent'
  AND f."subject" = 'world'
  AND f."status" = 'active'
  AND f."source" = 'reflect'
ON CONFLICT ("fact_id") DO NOTHING;

CREATE TABLE "skill_usage" (
  "skill_id" text NOT NULL,
  "user_id" uuid NOT NULL,
  "agent_id" text NOT NULL,
  "use_count" bigint NOT NULL DEFAULT 0,
  "last_used_at" timestamptz NOT NULL DEFAULT now(),
  "created_at" timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY ("skill_id"),
  CONSTRAINT "skill_usage_skill_id_fkey" FOREIGN KEY ("skill_id") REFERENCES "skill" ("id") ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT "skill_usage_user_id_fkey" FOREIGN KEY ("user_id") REFERENCES "auth_user" ("id") ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT "skill_usage_agent_id_fkey" FOREIGN KEY ("agent_id") REFERENCES "agent" ("id") ON UPDATE NO ACTION ON DELETE CASCADE
);

CREATE INDEX "idx_skill_usage_last_used" ON "skill_usage" ("last_used_at", "use_count", "skill_id");

-- Same boundary as knowledge: seed usage for already-marked Reflect-owned
-- skills only. Unmarked legacy skills remain outside curator ownership.
INSERT INTO "skill_usage" ("skill_id", "user_id", "agent_id", "use_count", "last_used_at", "created_at")
SELECT s."id", s."user_id", s."agent_id", 1, now(), now()
FROM "skill" s
WHERE s."scope" = 'user_agent'
  AND s."status" = 'active'
  AND s."metadata"->>'created_by' = 'reflect'
ON CONFLICT ("skill_id") DO NOTHING;

ALTER TABLE "skill_changelog" DROP CONSTRAINT "skill_changelog_action_check";
ALTER TABLE "skill_changelog"
  ADD CONSTRAINT "skill_changelog_action_check" CHECK ("action" IN ('create', 'patch', 'deprecate', 'restore'));

-- +goose Down
DELETE FROM "skill_changelog" WHERE "action" IN ('deprecate', 'restore');

ALTER TABLE "skill_changelog" DROP CONSTRAINT "skill_changelog_action_check";
ALTER TABLE "skill_changelog"
  ADD CONSTRAINT "skill_changelog_action_check" CHECK ("action" IN ('create', 'patch'));

DROP TABLE "skill_usage";
DROP TABLE "knowledge_usage";
