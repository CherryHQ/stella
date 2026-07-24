-- +goose Up

CREATE TABLE "knowledge_file" (
  "id" uuid NOT NULL DEFAULT uuidv7(),
  "scope" text NOT NULL,
  "user_id" uuid NULL,
  "agent_id" text NULL,
  "file_name" text NOT NULL,
  "media_type" text NOT NULL,
  "size_bytes" bigint NOT NULL,
  "raw_content" bytea NOT NULL,
  "status" text NOT NULL DEFAULT 'processing',
  "error_message" text NULL,
  "created_at" timestamptz NOT NULL DEFAULT now(),
  "updated_at" timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY ("id"),
  CONSTRAINT "knowledge_file_owner_check" CHECK (
    ((scope = 'system') AND (user_id IS NULL) AND (agent_id IS NULL))
    OR ((scope = 'system_agent') AND (user_id IS NULL) AND (agent_id IS NOT NULL))
    OR ((scope = 'user') AND (user_id IS NOT NULL) AND (agent_id IS NULL))
    OR ((scope = 'user_agent') AND (user_id IS NOT NULL) AND (agent_id IS NOT NULL))
  ),
  CONSTRAINT "knowledge_file_status_check" CHECK (
    status IN ('processing', 'ready', 'failed')
  ),
  CONSTRAINT "knowledge_file_error_check" CHECK (
    ((status = 'failed') AND (error_message IS NOT NULL) AND (btrim(error_message) <> '') AND (octet_length(error_message) <= 1024))
    OR ((status IN ('processing', 'ready')) AND (error_message IS NULL))
  ),
  CONSTRAINT "knowledge_file_name_check" CHECK (btrim(file_name) <> ''),
  CONSTRAINT "knowledge_file_media_type_check" CHECK (
    media_type IN (
      'application/pdf',
      'application/vnd.openxmlformats-officedocument.wordprocessingml.document',
      'text/markdown',
      'text/plain'
    )
  ),
  CONSTRAINT "knowledge_file_size_check" CHECK (
    size_bytes >= 0
    AND size_bytes <= 26214400
    AND octet_length(raw_content) = size_bytes
  ),
  CONSTRAINT "knowledge_file_user_id_fkey" FOREIGN KEY ("user_id") REFERENCES "auth_user" ("id") ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT "knowledge_file_agent_id_fkey" FOREIGN KEY ("agent_id") REFERENCES "agent" ("id") ON UPDATE NO ACTION ON DELETE CASCADE
);

-- Covers user/user_agent lists, the shared personal quota pool, and user deletion.
CREATE INDEX "idx_knowledge_file_user_owner" ON "knowledge_file"
  ("user_id", "scope", "agent_id", "created_at" DESC, "id" DESC)
  INCLUDE ("size_bytes")
  WHERE "user_id" IS NOT NULL;

-- Covers system_agent lists and Agent deletion. user_agent lists use the user index.
CREATE INDEX "idx_knowledge_file_agent_owner" ON "knowledge_file"
  ("agent_id", "scope", "created_at" DESC, "id" DESC)
  INCLUDE ("size_bytes")
  WHERE "agent_id" IS NOT NULL;

-- system is the only owner tuple without a user or Agent key.
CREATE INDEX "idx_knowledge_file_system_created" ON "knowledge_file"
  ("created_at" DESC, "id" DESC)
  INCLUDE ("size_bytes")
  WHERE "scope" = 'system';

CREATE TABLE "knowledge_chunk" (
  "id" uuid NOT NULL DEFAULT uuidv7(),
  "file_id" uuid NOT NULL,
  "ordinal" bigint NOT NULL,
  "content" text NOT NULL,
  "locator" jsonb NOT NULL DEFAULT '{}',
  PRIMARY KEY ("id"),
  CONSTRAINT "knowledge_chunk_file_id_fkey" FOREIGN KEY ("file_id") REFERENCES "knowledge_file" ("id") ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT "knowledge_chunk_ordinal_check" CHECK ("ordinal" >= 0),
  CONSTRAINT "knowledge_chunk_content_check" CHECK (btrim("content") <> ''),
  CONSTRAINT "knowledge_chunk_file_ordinal_key" UNIQUE ("file_id", "ordinal")
);

CREATE INDEX "idx_knowledge_chunk_bm25" ON "knowledge_chunk" USING bm25 ("id", "content")
  WITH (
    key_field = 'id',
    text_fields = '{"content":{"tokenizer":{"type":"jieba","stopwords":[" ","\t","\n","\r"]}}}'
  );

-- +goose Down

DROP TABLE "knowledge_chunk";
DROP TABLE "knowledge_file";
