-- +goose NO TRANSACTION

-- +goose Up
SET lock_timeout = '5s';
SET statement_timeout = '15min';

-- IF NOT EXISTS lets Goose safely retry this non-transactional migration after
-- a later concurrent index build or constraint validation is interrupted.
ALTER TABLE "ctx_group_message"
  ADD COLUMN IF NOT EXISTS "actor_display_name" text NULL;

ALTER TABLE "ctx_message"
  ADD COLUMN IF NOT EXISTS "origin_group_message_id" uuid NULL;

-- +goose StatementBegin
DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1
    FROM pg_constraint
    WHERE conrelid = 'ctx_message'::regclass
      AND conname = 'ctx_message_origin_group_message_id_fkey'
  ) THEN
    ALTER TABLE "ctx_message"
      ADD CONSTRAINT "ctx_message_origin_group_message_id_fkey"
      FOREIGN KEY ("origin_group_message_id")
      REFERENCES "ctx_group_message" ("id")
      ON UPDATE NO ACTION
      ON DELETE SET NULL
      NOT VALID;
  END IF;
END
$$;
-- +goose StatementEnd

-- Remove an invalid index left by an interrupted concurrent build before retrying.
DROP INDEX CONCURRENTLY IF EXISTS "idx_ctx_message_conversation_group_origin";
CREATE UNIQUE INDEX CONCURRENTLY "idx_ctx_message_conversation_group_origin"
ON "ctx_message" ("conversation_id", "origin_group_message_id")
WHERE "origin_group_message_id" IS NOT NULL;

ALTER TABLE "ctx_message"
  VALIDATE CONSTRAINT "ctx_message_origin_group_message_id_fkey";

-- Keep group and sequence fields in the BM25 index so pg_search can push the
-- trusted group/trigger filters into the same bounded search plan.
DROP INDEX CONCURRENTLY IF EXISTS "idx_ctx_group_message_bm25";
CREATE INDEX CONCURRENTLY "idx_ctx_group_message_bm25"
ON "ctx_group_message" USING bm25 (
  "id", "group_id", "seq", "platform_timestamp", "created_at", "content", "actor_display_name"
)
WITH (
  key_field = 'id',
  text_fields = '{"content":{"tokenizer":{"type":"jieba","stopwords":[" ","\t","\n","\r"]}},"actor_display_name":{"tokenizer":{"type":"jieba","stopwords":[" ","\t","\n","\r"]}}}'
);

-- The canonical public event log plus model-initiated recall replaces this
-- unstructured derived drawer. Its data is intentionally not migrated.
DROP TABLE IF EXISTS "ctx_group_memory";

RESET statement_timeout;
RESET lock_timeout;

-- +goose Down
SET lock_timeout = '5s';

CREATE TABLE IF NOT EXISTS "ctx_group_memory" (
  "group_id" uuid NOT NULL,
  "content" text NOT NULL DEFAULT '',
  "version" bigint NOT NULL DEFAULT 0,
  "created_at" timestamptz NOT NULL DEFAULT now(),
  "updated_at" timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY ("group_id"),
  CONSTRAINT "ctx_group_memory_group_id_fkey"
    FOREIGN KEY ("group_id") REFERENCES "ctx_group_state" ("id")
    ON UPDATE NO ACTION ON DELETE CASCADE
);

DROP INDEX CONCURRENTLY IF EXISTS "idx_ctx_group_message_bm25";

ALTER TABLE "ctx_message"
  DROP CONSTRAINT IF EXISTS "ctx_message_origin_group_message_id_fkey";

DROP INDEX CONCURRENTLY IF EXISTS "idx_ctx_message_conversation_group_origin";

ALTER TABLE "ctx_message"
  DROP COLUMN IF EXISTS "origin_group_message_id";

ALTER TABLE "ctx_group_message"
  DROP COLUMN IF EXISTS "actor_display_name";

RESET lock_timeout;
