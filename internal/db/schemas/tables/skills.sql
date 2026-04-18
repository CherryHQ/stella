CREATE TABLE skills (
    id          TEXT PRIMARY KEY,
    scope       TEXT NOT NULL CHECK (scope IN ('system','agent','user','project')),
    user_id     INTEGER REFERENCES auth_users(id) ON DELETE CASCADE,
    agent_id    TEXT    REFERENCES settings_agents(id) ON DELETE CASCADE,
    project     TEXT,
    name        TEXT NOT NULL,
    description TEXT NOT NULL,
    status      TEXT NOT NULL DEFAULT 'active'
                CHECK (status IN ('draft','active','deprecated')),
    disable_model_invocation INTEGER NOT NULL DEFAULT 0,
    metadata    TEXT NOT NULL DEFAULT '{}',
    created_at  TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at  TEXT NOT NULL DEFAULT (datetime('now')),

    CHECK (
        (scope='system'  AND user_id IS NULL     AND agent_id IS NULL     AND project IS NULL) OR
        (scope='agent'   AND user_id IS NULL     AND agent_id IS NOT NULL AND project IS NULL) OR
        (scope='user'    AND user_id IS NOT NULL AND agent_id IS NULL     AND project IS NULL) OR
        (scope='project' AND user_id IS NULL     AND agent_id IS NULL     AND project IS NOT NULL)
    )
);

CREATE UNIQUE INDEX idx_skills_owner_name
    ON skills (name, scope, ifnull(user_id, 0), ifnull(agent_id, ''), ifnull(project, ''));

CREATE INDEX idx_skills_visibility
    ON skills (scope, user_id, agent_id, project);
