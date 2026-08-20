-- +goose Up
-- Optimistic group collaboration: caps and freshness bookkeeping on the group,
-- delivery state on the message, and wake bookkeeping on the dispatch row.
ALTER TABLE ctx_group_state
    ADD COLUMN agent_chain_hard_limit INT NOT NULL DEFAULT 8,
    ADD COLUMN max_agent_posts_per_minute INT NOT NULL DEFAULT 10,
    ADD COLUMN max_replies_per_human_trigger INT NOT NULL DEFAULT 5,
    ADD COLUMN hold_limit INT NOT NULL DEFAULT 3,
    ADD COLUMN nudge_at TIMESTAMPTZ,
    -- nudge_at records when a nudge was sent; nudge_checked_at records when the
    -- classifier last looked. Without the second one an idle group is re-asked
    -- every tick for as long as it stays idle, and the answer cannot change
    -- until somebody speaks.
    ADD COLUMN nudge_checked_at TIMESTAMPTZ,
    -- Consecutive nudges since the last human or agent message. Any real
    -- message resets it, so it bounds a nudge loop without bounding a group.
    ADD COLUMN nudge_streak_count INT NOT NULL DEFAULT 0;

ALTER TABLE ctx_group_message
    ADD COLUMN delivery_state TEXT NOT NULL DEFAULT 'delivered';

-- Existing rows adopt the wake default: routing decided before this migration
-- is re-decided by per-agent triage, and live leases keep running.
-- publish_started_at separates "publish never ran" from "publish ran and we
-- never saw its outcome". Only the second is ambiguous after a crash, and the
-- recovery path chooses a possible duplicate over a lost reply there.
ALTER TABLE ctx_group_dispatch
    ADD COLUMN kind TEXT NOT NULL DEFAULT 'wake',
    ADD COLUMN trigger_seq BIGINT,
    ADD COLUMN held_up_to_seq BIGINT,
    ADD COLUMN publish_started_at TIMESTAMPTZ,
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

-- +goose StatementBegin
-- ctx_group_chain_root is the single definition of where one agent's current
-- causal chain starts: a later human message, or this agent's own accepted
-- post, opens a new chain. Four consumers share it: the wake claim gate, HOLD
-- count, held-coverage hint, and chain-scoped verbatim dedup. They stand on
-- different failure modes: relaxing only the gate lets a held row re-run and
-- post twice, tightening only the count makes a HOLD never expire, and widening
-- only dedup can suppress an ordinary acknowledgement forever. Keeping one
-- definition is what stops those consumers drifting.
CREATE FUNCTION ctx_group_chain_root(p_group_id UUID, p_agent_id TEXT, p_trigger_seq BIGINT)
RETURNS BIGINT
LANGUAGE sql STABLE PARALLEL SAFE AS $$
    SELECT GREATEST(
        COALESCE((
            SELECT MAX(own.seq)
            FROM ctx_group_dispatch accepted
            -- Legacy/non-published rows carry the empty-string sentinel. Cast
            -- only a real accepted message id, otherwise this gate poisons all
            -- wake claims with invalid UUID syntax.
            JOIN ctx_group_message own ON own.id = NULLIF(accepted.result_message_id, '')::uuid
            WHERE accepted.group_id = p_group_id
              AND accepted.agent_id = p_agent_id
        ), 0),
        COALESCE((
            SELECT MAX(human.seq)
            FROM ctx_group_message human
            WHERE human.group_id = p_group_id
              AND human.actor_type = 'human'
              AND human.seq <= p_trigger_seq
        ), 0)
    );
$$;
-- +goose StatementEnd

-- Durable work claims: one live owner per (group, key); leases expire so a
-- crashed owner cannot strand the work.
CREATE TABLE ctx_group_claim (
    id UUID PRIMARY KEY DEFAULT uuidv7(),
    group_id UUID NOT NULL REFERENCES ctx_group_state(id) ON DELETE CASCADE,
    key TEXT NOT NULL,
    owner_agent_id TEXT NOT NULL REFERENCES agent(id) ON DELETE CASCADE,
    note TEXT NOT NULL DEFAULT '',
    lease_until TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (group_id, key)
);

CREATE INDEX idx_ctx_group_claim_group_lease_until
    ON ctx_group_claim (group_id, lease_until);

-- +goose Down
DROP TABLE ctx_group_claim;
DROP FUNCTION ctx_group_chain_root(UUID, TEXT, BIGINT);
DROP INDEX idx_ctx_group_dispatch_wake_newest;
ALTER TABLE ctx_group_dispatch
    DROP COLUMN published_at,
    DROP COLUMN publish_started_at,
    DROP COLUMN held_up_to_seq,
    DROP COLUMN trigger_seq,
    DROP COLUMN kind;
ALTER TABLE ctx_group_message DROP COLUMN delivery_state;
ALTER TABLE ctx_group_state
    DROP COLUMN nudge_streak_count,
    DROP COLUMN nudge_checked_at,
    DROP COLUMN nudge_at,
    DROP COLUMN hold_limit,
    DROP COLUMN max_replies_per_human_trigger,
    DROP COLUMN max_agent_posts_per_minute,
    DROP COLUMN agent_chain_hard_limit;
