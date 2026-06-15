CREATE TABLE vault_entry (
    id         TEXT PRIMARY KEY,
    scope      TEXT NOT NULL DEFAULT 'user',
    user_id    TEXT REFERENCES auth_user(id) ON DELETE CASCADE,
    agent_id   TEXT REFERENCES agent(id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    ciphertext TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now')),
    CHECK (
        (scope = 'user'         AND user_id IS NOT NULL AND agent_id IS NULL) OR
        (scope = 'user_agent'   AND user_id IS NOT NULL AND agent_id IS NOT NULL) OR
        (scope = 'system'       AND user_id IS NULL     AND agent_id IS NULL) OR
        (scope = 'system_agent' AND user_id IS NULL     AND agent_id IS NOT NULL)
    )
);

CREATE UNIQUE INDEX uniq_vault_entry_scope_key
    ON vault_entry (scope, ifnull(user_id, ''), ifnull(agent_id, ''), name);

CREATE INDEX idx_vault_entry_scope
    ON vault_entry (scope, user_id, agent_id);
