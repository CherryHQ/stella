-- +goose Up
-- Monotonic per-session event counter. ctx_session_event rows are pruned by
-- retention, so MAX(seq)+1 would reset; this counter lives on the session row
-- and never shrinks, keeping reconnect cursors unambiguous across prunes.
ALTER TABLE ctx_conversation ADD COLUMN event_seq BIGINT NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE ctx_conversation DROP COLUMN event_seq;
