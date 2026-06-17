CREATE TABLE skill (
    id          TEXT PRIMARY KEY,
    scope       TEXT NOT NULL,
    user_id     TEXT    REFERENCES auth_user(id) ON DELETE CASCADE,
    agent_id    TEXT    REFERENCES agent(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    description TEXT NOT NULL,
    status      TEXT NOT NULL DEFAULT 'active',
    disable_model_invocation BOOLEAN NOT NULL DEFAULT false,
    metadata    JSONB NOT NULL DEFAULT '{}',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),

    CHECK (
        (scope='user'         AND user_id IS NOT NULL AND agent_id IS NULL) OR
        (scope='user_agent'   AND user_id IS NOT NULL AND agent_id IS NOT NULL) OR
        (scope='system'       AND user_id IS NULL     AND agent_id IS NULL) OR
        (scope='system_agent' AND user_id IS NULL     AND agent_id IS NOT NULL)
    )
);

CREATE UNIQUE INDEX idx_skill_owner_name
    ON skill (name, scope, COALESCE(user_id, ''), COALESCE(agent_id, ''));

CREATE INDEX idx_skill_visibility
    ON skill (scope, user_id, agent_id);
