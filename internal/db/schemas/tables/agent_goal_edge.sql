-- Accepted-output dependency between SIBLING goals. Only ACCEPTED output
-- flows downstream. hard blocks readiness; soft is advisory. Cycle prevention is in
-- the service (DFS), not the DB. Waiver trio carried verbatim from agent_task_dep.
CREATE TABLE agent_goal_edge (
    goal_id      TEXT NOT NULL REFERENCES agent_goal(id) ON DELETE CASCADE, -- downstream (dependent)
    upstream_id         TEXT NOT NULL REFERENCES agent_goal(id) ON DELETE CASCADE, -- must be accepted to satisfy
    edge_kind           TEXT NOT NULL DEFAULT 'hard',                          -- hard|soft (Go-enforced)
    on_failure          TEXT NOT NULL DEFAULT 'block',                         -- block|fail|ignore (Go-enforced)
    waived_at           TEXT,
    waived_by_user      TEXT REFERENCES auth_user(id) ON DELETE SET NULL,
    waiver_reason       TEXT NOT NULL DEFAULT '',
    created_at          TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (goal_id, upstream_id),
    CHECK (goal_id != upstream_id)                                      -- no self-edge (self-ref)
);

CREATE INDEX idx_agent_goal_edge_upstream ON agent_goal_edge(upstream_id);
