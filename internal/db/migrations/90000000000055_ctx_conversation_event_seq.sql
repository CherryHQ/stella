-- +goose Up
-- Monotonic per-session event counter. ctx_session_event rows are pruned by
-- retention, so MAX(seq)+1 would reset; this counter lives on the session row
-- and never shrinks, keeping reconnect cursors unambiguous across prunes.
ALTER TABLE ctx_conversation ADD COLUMN event_seq BIGINT NOT NULL DEFAULT 0;
-- Existing logs may already have events; the counter must continue past them.
UPDATE ctx_conversation c
SET event_seq = COALESCE((SELECT MAX(e.seq) FROM ctx_session_event e WHERE e.session_id = c.session_id), 0);

-- +goose Down
ALTER TABLE ctx_conversation DROP COLUMN event_seq;
