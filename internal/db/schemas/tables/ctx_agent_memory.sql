CREATE TABLE ctx_agent_memory (
    user_id          TEXT NOT NULL REFERENCES auth_user(id) ON DELETE CASCADE,
    agent_id         TEXT NOT NULL REFERENCES agent(id) ON DELETE CASCADE,
    content          TEXT NOT NULL DEFAULT '',
    soul             TEXT NOT NULL DEFAULT '',
    version          BIGINT NOT NULL DEFAULT 0,
    constraints      JSONB NOT NULL DEFAULT '[]',
    -- JSON array of auto-generated dated profile entries (D5/D6). Manual edits
    -- go through 'content'; async ingest appends here. PATCH never touches this
    -- column, so manual writes cannot erase auto-extracted entries.
    profile_entries  JSONB NOT NULL DEFAULT '[]',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY(user_id, agent_id)
);
