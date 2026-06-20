CREATE TABLE project (
    id UUID PRIMARY KEY DEFAULT uuidv7(),
    agent_id TEXT NOT NULL REFERENCES agent(id) ON DELETE CASCADE,
    user_id UUID NOT NULL REFERENCES auth_user(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    base_dir TEXT NOT NULL,
    description TEXT,
    archived BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(agent_id, user_id, name)
);
