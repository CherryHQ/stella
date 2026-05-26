CREATE TABLE auth_user_agent (
    user_id  TEXT NOT NULL REFERENCES auth_user(id) ON DELETE CASCADE,
    agent_id TEXT NOT NULL REFERENCES settings_agent(id) ON DELETE CASCADE,
    PRIMARY KEY(user_id, agent_id)
);
