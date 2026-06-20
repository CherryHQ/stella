-- Create "search_embedding" table
CREATE TABLE "search_embedding" (
  "id" uuid NOT NULL DEFAULT uuidv7(),
  "owner_kind" text NOT NULL,
  "owner_id" text NOT NULL,
  "model" text NOT NULL,
  "dims" bigint NOT NULL,
  "content_hash" bytea NOT NULL,
  "embedding" bytea NOT NULL,
  "created_at" timestamptz NOT NULL DEFAULT now(),
  "updated_at" timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY ("id"),
  CONSTRAINT "search_embedding_owner_kind_owner_id_model_key" UNIQUE ("owner_kind", "owner_id", "model"),
  CONSTRAINT "search_embedding_dims_check" CHECK (dims > 0)
);
-- Create index "idx_search_embedding_model_updated" to table: "search_embedding"
CREATE INDEX "idx_search_embedding_model_updated" ON "search_embedding" ("model", "updated_at");
-- Create index "idx_search_embedding_owner" to table: "search_embedding"
CREATE INDEX "idx_search_embedding_owner" ON "search_embedding" ("owner_kind", "owner_id");
