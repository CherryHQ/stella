-- +goose Up
-- The public recall lane searches only canonical text projections. jieba keeps
-- Chinese and mixed-language queries on the same indexed BM25 path as DM recall.
CREATE INDEX idx_ctx_group_message_bm25
    ON ctx_group_message USING bm25 (id, content, actor_display_name)
    WITH (key_field = 'id', text_fields = '{"content":{"tokenizer":{"type":"jieba","stopwords":[" ","\\t","\\n","\\r"]}},"actor_display_name":{"tokenizer":{"type":"jieba","stopwords":[" ","\\t","\\n","\\r"]}}}');

-- +goose Down
DROP INDEX idx_ctx_group_message_bm25;
