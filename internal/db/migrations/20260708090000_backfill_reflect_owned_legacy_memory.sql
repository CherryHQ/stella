-- +goose Up
-- Backfill only facts that were created by the facts migration from legacy
-- profile/soul blobs and whose latest legacy identity changelog proves Reflect
-- was the writer. Migration metadata alone is not provenance.
WITH latest_legacy_identity AS (
  SELECT DISTINCT ON ("user_id", "agent_id", "scope")
    "user_id",
    "agent_id",
    "scope",
    "source",
    "after_text"
  FROM "ctx_agent_memory_changelog"
  WHERE "scope" IN ('profile', 'soul')
    AND "memory_version_after" IS NOT NULL
  ORDER BY
    "user_id",
    "agent_id",
    "scope",
    "memory_version_after" DESC NULLS LAST,
    "created_at" DESC,
    "id" DESC
),
eligible_facts AS (
  SELECT f."id"
  FROM "facts" f
  JOIN latest_legacy_identity l
    ON l."user_id" = f."user_id"
   AND l."agent_id" = f."agent_id"
   AND (
     (f."subject" = 'user' AND l."scope" = 'profile')
     OR (f."subject" = 'agent' AND l."scope" = 'soul')
   )
  WHERE f."source" = 'manual'
    AND f."status" = 'active'
    AND l."source" = 'reflect'
    AND l."after_text" = f."content"
    AND EXISTS (
      SELECT 1
      FROM "ctx_agent_memory_changelog" c
      WHERE c."scope" = 'fact'
        AND c."entity_id" = f."id"::text
        AND c."metadata" IS NOT NULL
        AND (c."metadata"::jsonb)->>'migration' = '20260625090000_add_facts_memory'
    )
),
updated_facts AS (
  UPDATE "facts" f
  SET "source" = 'reflect',
      "updated_at" = now()
  FROM eligible_facts e
  WHERE f."id" = e."id"
  RETURNING f."id"
)
UPDATE "ctx_agent_memory_changelog" c
SET "source" = 'reflect',
    "after_text" = CASE
      WHEN c."after_text" IS NULL THEN NULL
      ELSE jsonb_set(c."after_text"::jsonb, '{source}', '"reflect"'::jsonb, true)::text
    END,
    "metadata" = jsonb_set(
      COALESCE(c."metadata"::jsonb, '{}'::jsonb),
      '{reflect_provenance_backfill}',
      '"20260708090000_backfill_reflect_owned_legacy_memory"'::jsonb,
      true
    )::text
WHERE c."scope" = 'fact'
  AND c."entity_id" IN (SELECT "id"::text FROM updated_facts)
  AND c."metadata" IS NOT NULL
  AND (c."metadata"::jsonb)->>'migration' = '20260625090000_add_facts_memory';

-- +goose Down
-- Roll back only rows that are still active. If Reflect or a manual migration
-- later superseded/deprecated a backfilled row, keep that later audit history
-- intact instead of rewriting provenance on an obsolete fact.
WITH marked_active_facts AS (
  SELECT c."entity_id"
  FROM "ctx_agent_memory_changelog" c
  JOIN "facts" f ON f."id"::text = c."entity_id"
  WHERE c."scope" = 'fact'
    AND c."metadata" IS NOT NULL
    AND (c."metadata"::jsonb)->>'reflect_provenance_backfill' = '20260708090000_backfill_reflect_owned_legacy_memory'
    AND f."status" = 'active'
),
reverted_facts AS (
  UPDATE "facts" f
  SET "source" = 'manual',
      "updated_at" = now()
  FROM marked_active_facts m
  WHERE f."id"::text = m."entity_id"
    AND f."source" = 'reflect'
  RETURNING f."id"
)
UPDATE "ctx_agent_memory_changelog" c
SET "source" = 'manual',
    "after_text" = CASE
      WHEN c."after_text" IS NULL THEN NULL
      ELSE jsonb_set(c."after_text"::jsonb, '{source}', '"manual"'::jsonb, true)::text
    END,
    "metadata" = (c."metadata"::jsonb - 'reflect_provenance_backfill')::text
WHERE c."scope" = 'fact'
  AND c."entity_id" IN (SELECT "id"::text FROM reverted_facts)
  AND c."metadata" IS NOT NULL
  AND (c."metadata"::jsonb)->>'reflect_provenance_backfill' = '20260708090000_backfill_reflect_owned_legacy_memory';
