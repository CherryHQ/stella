-- ctx_group_memory stores shared memory for a group conversation. Keyed by
-- group_id only (no agent dimension): all agents in the group read the same
-- shared knowledge. Private per-user memory stays in ctx_agent_memory, and the
-- two tables are written by separate code paths so DM writes cannot leak into
-- group memory (type-level privacy wall).
CREATE TABLE ctx_group_memory (
    group_id    TEXT NOT NULL REFERENCES ctx_group_state(id) ON DELETE CASCADE,
    content     TEXT NOT NULL DEFAULT '',
    version     INTEGER NOT NULL DEFAULT 0,
    created_at  TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at  TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY(group_id)
);
