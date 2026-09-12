-- +goose Up
-- Execution coordinate for the durable event log: events produced under a
-- session execution lease carry its token so observers can tail turns that
-- never had an agent_run (scheduler, delegate, session.send). The column is
-- deliberately reference-free — the execution row is deleted on finish, and
-- the marker/event rows must outlive it.
ALTER TABLE ctx_session_event
    ADD COLUMN execution_id UUID;

-- Tail-by-execution scan: WHERE session_id=$ AND execution_id=$ AND seq>$.
CREATE INDEX idx_ctx_session_event_execution
    ON ctx_session_event (session_id, execution_id, seq)
    WHERE execution_id IS NOT NULL;

-- +goose Down
ALTER TABLE ctx_session_event DROP COLUMN execution_id;
