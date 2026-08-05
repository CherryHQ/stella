-- +goose Up

CREATE TABLE "knowledge_file" (
  "id" uuid NOT NULL DEFAULT uuidv7(),
  "scope" text NOT NULL,
  "user_id" uuid NULL,
  "agent_id" text NULL,
  "file_name" text NOT NULL,
  "media_type" text NOT NULL,
  "size_bytes" bigint NOT NULL,
  "raw_sha256" bytea NOT NULL,
  "status" text NOT NULL DEFAULT 'processing',
  "error_message" text NULL,
  "active_chunk_set_id" uuid NULL,
  "deleted_at" timestamptz NULL,
  "created_at" timestamptz NOT NULL DEFAULT now(),
  "updated_at" timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY ("id"),
  CONSTRAINT "knowledge_file_owner_check" CHECK (
    ((scope = 'system') AND (user_id IS NULL) AND (agent_id IS NULL))
    OR ((scope = 'system_agent') AND (user_id IS NULL) AND (agent_id IS NOT NULL))
    OR ((scope = 'user') AND (user_id IS NOT NULL) AND (agent_id IS NULL))
    OR ((scope = 'user_agent') AND (user_id IS NOT NULL) AND (agent_id IS NOT NULL))
  ),
  CONSTRAINT "knowledge_file_size_check" CHECK (size_bytes >= 0),
  CONSTRAINT "knowledge_file_user_id_fkey" FOREIGN KEY ("user_id") REFERENCES "auth_user" ("id") ON DELETE CASCADE,
  CONSTRAINT "knowledge_file_agent_id_fkey" FOREIGN KEY ("agent_id") REFERENCES "agent" ("id") ON DELETE CASCADE
);

-- These narrow indexes cover every non-null FK reference, including tombstones,
-- so ON DELETE CASCADE does not degrade to a full table scan.
CREATE INDEX "idx_knowledge_file_user_id" ON "knowledge_file" ("user_id")
  WHERE "user_id" IS NOT NULL;

CREATE INDEX "idx_knowledge_file_agent_id" ON "knowledge_file" ("agent_id")
  WHERE "agent_id" IS NOT NULL;

-- These partial indexes serve management lists and exact logical quota pools.
-- Tombstoned files leave logical quota accounting immediately.
CREATE INDEX "idx_knowledge_file_user_owner" ON "knowledge_file"
  ("user_id", "scope", "agent_id", "created_at" DESC, "id" DESC)
  INCLUDE ("size_bytes")
  WHERE "user_id" IS NOT NULL AND "deleted_at" IS NULL;

CREATE INDEX "idx_knowledge_file_agent_owner" ON "knowledge_file"
  ("agent_id", "scope", "created_at" DESC, "id" DESC)
  INCLUDE ("size_bytes")
  WHERE "agent_id" IS NOT NULL AND "deleted_at" IS NULL;

CREATE INDEX "idx_knowledge_file_system_created" ON "knowledge_file"
  ("created_at" DESC, "id" DESC)
  INCLUDE ("size_bytes")
  WHERE "scope" = 'system' AND "deleted_at" IS NULL;

CREATE INDEX "idx_knowledge_file_processing" ON "knowledge_file" ("updated_at", "id")
  WHERE "status" = 'processing' AND "deleted_at" IS NULL;

CREATE INDEX "idx_knowledge_file_tombstone" ON "knowledge_file" ("deleted_at", "id")
  WHERE "deleted_at" IS NOT NULL;

CREATE TABLE "knowledge_chunk_set" (
  "id" uuid NOT NULL DEFAULT uuidv7(),
  "file_id" uuid NOT NULL,
  "derivation_key" text NOT NULL,
  "processor_key" text NOT NULL,
  "raw_sha256" bytea NOT NULL,
  "status" text NOT NULL DEFAULT 'building',
  "chunk_count" bigint NULL,
  "content_digest" bytea NULL,
  "error_message" text NULL,
  "created_at" timestamptz NOT NULL DEFAULT now(),
  "updated_at" timestamptz NOT NULL DEFAULT now(),
  "completed_at" timestamptz NULL,
  PRIMARY KEY ("id"),
  CONSTRAINT "knowledge_chunk_set_file_id_fkey" FOREIGN KEY ("file_id") REFERENCES "knowledge_file" ("id") ON DELETE CASCADE,
  CONSTRAINT "knowledge_chunk_set_file_derivation_key" UNIQUE ("file_id", "derivation_key"),
  CONSTRAINT "knowledge_chunk_set_file_id_id_key" UNIQUE ("file_id", "id")
);

-- The composite FK is the database-level publication invariant: an active set
-- can only belong to the same KnowledgeFile. The column-specific SET NULL keeps
-- file deletion and the ChunkSet cascade compatible.
ALTER TABLE "knowledge_file"
  ADD CONSTRAINT "knowledge_file_active_chunk_set_fkey"
  FOREIGN KEY ("id", "active_chunk_set_id")
  REFERENCES "knowledge_chunk_set" ("file_id", "id")
  ON DELETE SET NULL ("active_chunk_set_id")
  DEFERRABLE INITIALLY DEFERRED;

CREATE TABLE "knowledge_chunk" (
  "id" uuid NOT NULL DEFAULT uuidv7(),
  "chunk_set_id" uuid NOT NULL,
  "ordinal" bigint NOT NULL,
  "content" text NOT NULL,
  "locator" jsonb NOT NULL DEFAULT '{}',
  "content_sha256" bytea NOT NULL,
  "created_at" timestamptz NOT NULL DEFAULT now(),
  "updated_at" timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY ("id"),
  CONSTRAINT "knowledge_chunk_set_id_fkey" FOREIGN KEY ("chunk_set_id") REFERENCES "knowledge_chunk_set" ("id") ON DELETE CASCADE,
  CONSTRAINT "knowledge_chunk_set_ordinal_key" UNIQUE ("chunk_set_id", "ordinal")
);

CREATE INDEX "idx_knowledge_chunk_bm25" ON "knowledge_chunk" USING bm25 ("id", "content")
  WITH (
    key_field = 'id',
    text_fields = '{"content":{"tokenizer":{"type":"jieba","stopwords":[" ","\t","\n","\r"]}}}'
  );

-- +goose Down

DROP TABLE "knowledge_chunk";
ALTER TABLE "knowledge_file" DROP CONSTRAINT "knowledge_file_active_chunk_set_fkey";
DROP TABLE "knowledge_chunk_set";
DROP TABLE "knowledge_file";
