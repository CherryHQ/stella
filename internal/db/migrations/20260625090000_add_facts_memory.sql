-- +goose Up
-- Facts are the v1 long-term memory surface. Profile, soul, and knowledge are
-- represented by subject=user, subject=agent, and subject=world respectively.
CREATE TABLE "facts" (
  "id" uuid NOT NULL DEFAULT uuidv7(),
  "subject" text NOT NULL,
  "scope" text NOT NULL DEFAULT 'user_agent',
  "user_id" uuid NOT NULL,
  "agent_id" text NOT NULL,
  "content" text NOT NULL,
  "status" text NOT NULL DEFAULT 'active',
  "metadata" jsonb NOT NULL DEFAULT '{}',
  "supersedes" uuid NULL,
  "version" bigint NOT NULL DEFAULT 1,
  "source" text NOT NULL,
  "created_at" timestamptz NOT NULL DEFAULT now(),
  "updated_at" timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY ("id"),
  CONSTRAINT "facts_subject_check" CHECK ("subject" IN ('user', 'agent', 'world')),
  CONSTRAINT "facts_scope_check" CHECK ("scope" = 'user_agent'),
  CONSTRAINT "facts_status_check" CHECK ("status" IN ('active', 'deprecated')),
  CONSTRAINT "facts_source_check" CHECK ("source" IN ('reflect', 'manual')),
  CONSTRAINT "facts_user_id_fkey" FOREIGN KEY ("user_id") REFERENCES "auth_user" ("id") ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT "facts_agent_id_fkey" FOREIGN KEY ("agent_id") REFERENCES "agent" ("id") ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT "facts_supersedes_fkey" FOREIGN KEY ("supersedes") REFERENCES "facts" ("id") ON UPDATE NO ACTION ON DELETE SET NULL
);

CREATE INDEX "idx_facts_user_agent_subject_status" ON "facts" ("user_id", "agent_id", "subject", "status");
CREATE INDEX "idx_facts_supersedes" ON "facts" ("supersedes");

-- Profile and soul are singleton active facts per user-agent pair.
CREATE UNIQUE INDEX "uniq_facts_active_singleton_subject" ON "facts" ("user_id", "agent_id", "subject")
WHERE "status" = 'active' AND "subject" IN ('user', 'agent');

-- Backfill legacy profile blobs.
WITH inserted AS (
  INSERT INTO "facts" ("subject", "scope", "user_id", "agent_id", "content", "status", "metadata", "version", "source", "created_at", "updated_at")
  SELECT 'user', 'user_agent', "user_id", "agent_id", "content", 'active', '{}', 1, 'manual', "created_at", "updated_at"
  FROM "ctx_agent_memory"
  WHERE btrim("content") <> ''
  RETURNING *
)
INSERT INTO "ctx_agent_memory_changelog" ("id", "user_id", "agent_id", "entity_id", "scope", "action", "source", "memory_version_after", "after_text", "metadata", "created_at")
SELECT
  uuidv7(),
  f."user_id",
  f."agent_id",
  f."id"::text,
  'fact',
  'create',
  'manual',
  1,
  jsonb_build_object(
    'id', f."id"::text,
    'subject', f."subject",
    'scope', f."scope",
    'user_id', f."user_id"::text,
    'agent_id', f."agent_id",
    'content', f."content",
    'status', f."status",
    'metadata', f."metadata",
    'version', f."version",
    'source', f."source",
    'created_at', f."created_at",
    'updated_at', f."updated_at"
  )::text,
  jsonb_build_object(
    'migration', '20260625090000_add_facts_memory',
    'created_memory_row', false
  )::text,
  f."created_at"
FROM inserted f
JOIN "ctx_agent_memory" m ON m."user_id" = f."user_id" AND m."agent_id" = f."agent_id";

-- Backfill legacy soul blobs.
WITH inserted AS (
  INSERT INTO "facts" ("subject", "scope", "user_id", "agent_id", "content", "status", "metadata", "version", "source", "created_at", "updated_at")
  SELECT 'agent', 'user_agent', "user_id", "agent_id", "soul", 'active', '{}', 1, 'manual', "created_at", "updated_at"
  FROM "ctx_agent_memory"
  WHERE btrim("soul") <> ''
  RETURNING *
)
INSERT INTO "ctx_agent_memory_changelog" ("id", "user_id", "agent_id", "entity_id", "scope", "action", "source", "memory_version_after", "after_text", "metadata", "created_at")
SELECT
  uuidv7(),
  f."user_id",
  f."agent_id",
  f."id"::text,
  'fact',
  'create',
  'manual',
  1,
  jsonb_build_object(
    'id', f."id"::text,
    'subject', f."subject",
    'scope', f."scope",
    'user_id', f."user_id"::text,
    'agent_id', f."agent_id",
    'content', f."content",
    'status', f."status",
    'metadata', f."metadata",
    'version', f."version",
    'source', f."source",
    'created_at', f."created_at",
    'updated_at', f."updated_at"
  )::text,
  jsonb_build_object(
    'migration', '20260625090000_add_facts_memory',
    'created_memory_row', false
  )::text,
  f."created_at"
FROM inserted f
JOIN "ctx_agent_memory" m ON m."user_id" = f."user_id" AND m."agent_id" = f."agent_id";

-- Backfill user-agent scoped skill-backed knowledge entries. Other legacy
-- knowledge scopes are intentionally left in place because v1 facts only model
-- user_agent scope.
WITH legacy AS (
  SELECT
    s."user_id",
    s."agent_id",
    COALESCE(NULLIF(sf."content", ''), s."description", s."name") AS "content",
    CASE WHEN s."status" = 'deprecated' THEN 'deprecated' ELSE 'active' END AS "status",
    jsonb_build_object(
      'legacy_skill_id', s."id",
      'legacy_skill_name', s."name",
      'legacy_knowledge_type', s."metadata"->>'knowledge_type'
    ) AS "metadata",
    s."created_at",
    s."updated_at"
  FROM "skill" s
  LEFT JOIN "skill_file" sf ON sf."skill_id" = s."id" AND sf."path" = 'SKILL.md'
  WHERE s."disable_model_invocation" = true
    AND s."scope" = 'user_agent'
    AND s."user_id" IS NOT NULL
    AND s."agent_id" IS NOT NULL
    AND s."metadata"->>'knowledge_type' IN ('fact', 'context')
    AND COALESCE(NULLIF(sf."content", ''), s."description", s."name") <> ''
),
memory_rows AS (
  INSERT INTO "ctx_agent_memory" ("user_id", "agent_id", "version")
  SELECT DISTINCT "user_id", "agent_id", 1
  FROM legacy
  ON CONFLICT ("user_id", "agent_id") DO NOTHING
  RETURNING "user_id", "agent_id"
),
inserted AS (
  INSERT INTO "facts" ("subject", "scope", "user_id", "agent_id", "content", "status", "metadata", "version", "source", "created_at", "updated_at")
  SELECT 'world', 'user_agent', "user_id", "agent_id", "content", "status", "metadata", 1, 'manual', "created_at", "updated_at"
  FROM legacy
  RETURNING *
)
INSERT INTO "ctx_agent_memory_changelog" ("id", "user_id", "agent_id", "entity_id", "scope", "action", "source", "memory_version_after", "after_text", "metadata", "created_at")
SELECT
  uuidv7(),
  f."user_id",
  f."agent_id",
  f."id"::text,
  'fact',
  'create',
  'manual',
  1,
  jsonb_build_object(
    'id', f."id"::text,
    'subject', f."subject",
    'scope', f."scope",
    'user_id', f."user_id"::text,
    'agent_id', f."agent_id",
    'content', f."content",
    'status', f."status",
    'metadata', f."metadata",
    'version', f."version",
    'source', f."source",
    'created_at', f."created_at",
    'updated_at', f."updated_at"
  )::text,
  jsonb_build_object(
    'migration', '20260625090000_add_facts_memory',
    'created_memory_row', mr."user_id" IS NOT NULL
  )::text,
  f."created_at"
FROM inserted f
JOIN "ctx_agent_memory" m ON m."user_id" = f."user_id" AND m."agent_id" = f."agent_id"
LEFT JOIN memory_rows mr ON mr."user_id" = f."user_id" AND mr."agent_id" = f."agent_id";

-- +goose Down
DELETE FROM "ctx_agent_memory" m
USING "ctx_agent_memory_changelog" c
WHERE c."scope" = 'fact'
  AND c."user_id" = m."user_id"
  AND c."agent_id" = m."agent_id"
  AND (c."metadata"::jsonb)->>'migration' = '20260625090000_add_facts_memory'
  AND (c."metadata"::jsonb)->>'created_memory_row' = 'true'
  AND m."content" = ''
  AND m."soul" = ''
  AND m."version" = 1
  AND m."constraints" = '[]'::jsonb
  AND m."profile_entries" = '[]'::jsonb;

DELETE FROM "ctx_agent_memory_changelog"
WHERE "scope" = 'fact'
  AND ("metadata"::jsonb)->>'migration' = '20260625090000_add_facts_memory';

DROP TABLE IF EXISTS "facts";
