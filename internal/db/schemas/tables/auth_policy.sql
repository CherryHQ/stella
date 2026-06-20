CREATE TABLE auth_policy (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    effect     TEXT NOT NULL CHECK (effect IN ('allow', 'deny')),
    subjects   TEXT NOT NULL DEFAULT '{}',
    actions    TEXT NOT NULL DEFAULT '[]',
    resources  TEXT NOT NULL DEFAULT '[]',
    conditions TEXT NOT NULL DEFAULT '{}',
    priority   BIGINT NOT NULL DEFAULT 0,
    is_system  BOOLEAN NOT NULL DEFAULT false,
    enabled    BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
