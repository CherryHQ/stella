-- +goose Up
-- These columns are deliberately additive.  Phase 1 continues to execute only
-- reply rows, so an older binary fails closed if a later release leaves wake
-- work behind during a rollback.
ALTER TABLE ctx_group_state
    ADD COLUMN agent_chain_hard_limit INT NOT NULL DEFAULT 8,
    ADD COLUMN max_agent_posts_per_minute INT NOT NULL DEFAULT 10,
    ADD COLUMN max_replies_per_human_trigger INT NOT NULL DEFAULT 5,
    ADD COLUMN hold_limit INT NOT NULL DEFAULT 3,
    ADD COLUMN nudge_at TIMESTAMPTZ,
    ADD COLUMN nudge_fallback_count INT NOT NULL DEFAULT 0;

ALTER TABLE ctx_group_message
    ADD COLUMN delivery_state TEXT NOT NULL DEFAULT 'delivered';

ALTER TABLE ctx_group_dispatch
    ADD COLUMN kind TEXT NOT NULL DEFAULT 'reply',
    ADD COLUMN trigger_seq BIGINT,
    ADD COLUMN held_up_to_seq BIGINT,
    ADD COLUMN published_at TIMESTAMPTZ;

UPDATE ctx_group_dispatch AS dispatch
SET trigger_seq = message.seq
FROM ctx_group_message AS message
WHERE message.id = dispatch.group_message_id;

ALTER TABLE ctx_group_dispatch
    ALTER COLUMN trigger_seq SET NOT NULL;

-- ClaimNewestGroupWake needs newest-first lookup per group/agent.  The
-- existing pending index orders global retry work and cannot serve that shape.
CREATE INDEX idx_ctx_group_dispatch_wake_newest
    ON ctx_group_dispatch (group_id, agent_id, trigger_seq DESC)
    WHERE kind = 'wake' AND status = 'pending';

-- +goose Down
DROP INDEX idx_ctx_group_dispatch_wake_newest;
ALTER TABLE ctx_group_dispatch
    DROP COLUMN published_at,
    DROP COLUMN held_up_to_seq,
    DROP COLUMN trigger_seq,
    DROP COLUMN kind;
ALTER TABLE ctx_group_message DROP COLUMN delivery_state;
ALTER TABLE ctx_group_state
    DROP COLUMN nudge_fallback_count,
    DROP COLUMN nudge_at,
    DROP COLUMN hold_limit,
    DROP COLUMN max_replies_per_human_trigger,
    DROP COLUMN max_agent_posts_per_minute,
    DROP COLUMN agent_chain_hard_limit;
