CREATE TABLE channel (
    id         TEXT NOT NULL PRIMARY KEY,
    name       TEXT NOT NULL DEFAULT '',
    type       TEXT NOT NULL DEFAULT '',
    agent_id   TEXT REFERENCES agent(id) ON DELETE SET NULL,
    enabled    BOOLEAN NOT NULL DEFAULT true,
    config     TEXT NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
