-- +goose Up
-- Validate every persisted timestamp before rewriting state. Failing the
-- migration is safer than silently resetting a malformed session to zero and
-- replaying its full conversation history.
-- +goose StatementBegin
DO $$
BEGIN
  IF EXISTS (
    SELECT 1
    FROM "plugin_state"
    WHERE "plugin_id" = 'reflect'
      AND "scope_kind" = 'session'
      AND "state_key" IN (
        'review_watermark',
        'reflect_watermark:fact',
        'reflect_watermark:skill'
      )
      AND NULLIF("value"->>'reviewed_at', '') IS NOT NULL
      AND NOT (
        "value"->>'reviewed_at' ~ '^[0-9]{4}-[0-9]{2}-[0-9]{2} [0-9]{2}:[0-9]{2}:[0-9]{2}$'
        OR "value"->>'reviewed_at' ~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]+)?(Z|[+-][0-9]{2}:[0-9]{2})$'
      )
  ) THEN
    RAISE EXCEPTION 'invalid Reflect watermark timestamp';
  END IF;
END;
$$;
-- +goose StatementEnd

-- Seed both structured lines from the legacy global boundary. Existing line
-- state wins when it is at least as new; when legacy wins, omit reviewed_seq
-- because that sequence belongs to the older line boundary.
WITH "legacy_raw" AS (
  SELECT
    "plugin_id",
    "scope_kind",
    "scope_id",
    NULLIF("value"->>'reviewed_at', '') AS "reviewed_at_raw"
  FROM "plugin_state"
  WHERE "plugin_id" = 'reflect'
    AND "scope_kind" = 'session'
    AND "state_key" = 'review_watermark'
),
"legacy" AS (
  SELECT
    "plugin_id",
    "scope_kind",
    "scope_id",
    "reviewed_at_raw",
    CASE
      WHEN "reviewed_at_raw" IS NULL THEN NULL
      WHEN "reviewed_at_raw" ~ '^[0-9]{4}-[0-9]{2}-[0-9]{2} [0-9]{2}:[0-9]{2}:[0-9]{2}$'
        THEN "reviewed_at_raw"::timestamp AT TIME ZONE 'UTC'
      ELSE "reviewed_at_raw"::timestamptz
    END AS "reviewed_at"
  FROM "legacy_raw"
),
"desired" AS (
  SELECT
    l."plugin_id",
    l."scope_kind",
    l."scope_id",
    k."state_key",
    l."reviewed_at",
    CASE
      WHEN l."reviewed_at_raw" IS NULL THEN '{}'::jsonb
      ELSE jsonb_build_object('reviewed_at', l."reviewed_at_raw")
    END AS "value"
  FROM "legacy" l
  CROSS JOIN (
    VALUES ('reflect_watermark:fact'), ('reflect_watermark:skill')
  ) AS k("state_key")
),
"merged" AS (
  SELECT
    d."plugin_id",
    d."scope_kind",
    d."scope_id",
    d."state_key",
    CASE
      WHEN current."state_key" IS NULL THEN d."value"
      WHEN d."reviewed_at" IS NULL THEN current."value"
      WHEN NULLIF(current."value"->>'reviewed_at', '') IS NULL THEN d."value"
      WHEN (
        CASE
          WHEN current."value"->>'reviewed_at' ~ '^[0-9]{4}-[0-9]{2}-[0-9]{2} [0-9]{2}:[0-9]{2}:[0-9]{2}$'
            THEN (current."value"->>'reviewed_at')::timestamp AT TIME ZONE 'UTC'
          ELSE (current."value"->>'reviewed_at')::timestamptz
        END
      ) >= d."reviewed_at" THEN current."value"
      ELSE d."value"
    END AS "value"
  FROM "desired" d
  LEFT JOIN "plugin_state" current
    ON current."plugin_id" = d."plugin_id"
   AND current."scope_kind" = d."scope_kind"
   AND current."scope_id" = d."scope_id"
   AND current."state_key" = d."state_key"
)
INSERT INTO "plugin_state" (
  "plugin_id",
  "scope_kind",
  "scope_id",
  "state_key",
  "value"
)
SELECT
  "plugin_id",
  "scope_kind",
  "scope_id",
  "state_key",
  "value"
FROM "merged"
ON CONFLICT ("plugin_id", "scope_kind", "scope_id", "state_key")
DO UPDATE SET
  "value" = EXCLUDED."value",
  "updated_at" = now()
WHERE "plugin_state"."value" IS DISTINCT FROM EXCLUDED."value";

-- +goose Down
-- Intentional no-op. After cutover, Fact and Skill line watermarks advance
-- independently; restoring legacy values here could rewind progress. The old
-- global rows remain untouched for rollback to a previous binary.
SELECT 1;
