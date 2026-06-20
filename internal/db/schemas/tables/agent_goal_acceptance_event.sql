-- APPEND-ONLY acceptance ledger: every deterministic check result and every
-- judgment verdict, stored as evidence. agent_goal.acceptance_state is a
-- cached PROJECTION over these rows. A human "approve" is a verdict row here, never
-- a freely-mutated state column. Never updated/deleted in normal operation.
CREATE TABLE agent_goal_acceptance_event (
    id                  UUID NOT NULL PRIMARY KEY DEFAULT uuidv7(),
    goal_id      TEXT NOT NULL REFERENCES agent_goal(id) ON DELETE CASCADE,
    attempt_id          UUID REFERENCES agent_goal_attempt(id) ON DELETE SET NULL, -- the attempt whose output was evaluated
    seq                 BIGINT NOT NULL,                                       -- monotonic per goal; the projection folds in seq order

    item_id             TEXT NOT NULL,                                          -- contract item this result/verdict answers
    item_kind           TEXT NOT NULL,                                          -- 'deterministic'|'judgment' (Go-enforced)
    result              TEXT NOT NULL,                                          -- 'pass'|'fail' (Go-enforced) — the outcome for the item

    -- Deterministic detail.
    command             TEXT NOT NULL DEFAULT '',
    exit_code           BIGINT,                                               -- present iff item_kind='deterministic'
    cache_key           TEXT NOT NULL DEFAULT '',                              -- check-result cache key

    -- Judgment detail (the verdict, as evidence).
    authority           TEXT NOT NULL DEFAULT 'system',                        -- 'system'(deterministic)|'agent'|'human' (Go-enforced)
    reviewer_user_id    UUID REFERENCES auth_user(id) ON DELETE SET NULL,      -- set when authority='human'
    reviewer_attempt_id UUID REFERENCES agent_goal_attempt(id) ON DELETE SET NULL, -- the purpose='review' run, when authority='agent'
    rationale           TEXT NOT NULL DEFAULT '',                              -- why (asserted)
    scope               TEXT NOT NULL DEFAULT '',                              -- what the verdict covers (verdict staleness scope)
    scope_hash          TEXT NOT NULL DEFAULT '',                              -- accepted-output/artifact hash the verdict covers; '' for deterministic

    detail              JSONB NOT NULL DEFAULT '{}',                            -- truncated stdout / artifact hashes / gaps
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),

    CHECK (seq >= 0),                                                          -- range
    -- Column coupling: a deterministic check carries an exit_code; a judgment
    -- verdict does not (structural coupling, not an enumeration).
    CHECK ((item_kind = 'deterministic' AND exit_code IS NOT NULL)
           OR (item_kind = 'judgment' AND exit_code IS NULL))
);

CREATE INDEX idx_agent_goal_accept_evt_goal ON agent_goal_acceptance_event(goal_id, seq);
CREATE INDEX idx_agent_goal_accept_evt_attempt     ON agent_goal_acceptance_event(attempt_id);
-- Idempotent fold + dedup: one result per (goal, attempt, item, cache_key).
CREATE UNIQUE INDEX uniq_agent_goal_accept_evt
    ON agent_goal_acceptance_event(goal_id, attempt_id, item_id, cache_key);
-- Check-result cache probe: hit by cache_key on the latest pass.
CREATE INDEX idx_agent_goal_accept_evt_cache
    ON agent_goal_acceptance_event(cache_key, created_at DESC)
    WHERE item_kind = 'deterministic' AND cache_key != '';
