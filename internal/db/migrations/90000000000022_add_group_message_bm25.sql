-- +goose NO TRANSACTION

-- +goose Up
SET lock_timeout = '5s';

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

RESET lock_timeout;

-- +goose Down
SET lock_timeout = '5s';
DROP INDEX CONCURRENTLY IF EXISTS "idx_ctx_group_message_bm25";
RESET lock_timeout;
