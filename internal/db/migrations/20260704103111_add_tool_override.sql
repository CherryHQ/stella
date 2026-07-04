-- +goose Up
CREATE TABLE "tool_override" (
    "id"         UUID PRIMARY KEY DEFAULT uuidv7(),
    "tool_name"  TEXT NOT NULL,
    "scope"      TEXT NOT NULL,
    "user_id"    UUID REFERENCES "auth_user" ("id") ON DELETE CASCADE,
    "agent_id"   TEXT REFERENCES "agent" ("id") ON DELETE CASCADE,
    "enabled"    BOOLEAN NOT NULL,
    "created_at" TIMESTAMPTZ NOT NULL DEFAULT now(),
    "updated_at" TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT "tool_override_check" CHECK (
        ((scope = 'user') AND (user_id IS NOT NULL) AND (agent_id IS NULL))
        OR ((scope = 'user_agent') AND (user_id IS NOT NULL) AND (agent_id IS NOT NULL))
        OR ((scope = 'system') AND (user_id IS NULL) AND (agent_id IS NULL))
        OR ((scope = 'system_agent') AND (user_id IS NULL) AND (agent_id IS NOT NULL))
    ),
    UNIQUE NULLS NOT DISTINCT ("tool_name", "scope", "user_id", "agent_id")
);
CREATE INDEX "idx_tool_override_visibility" ON "tool_override" ("scope", "user_id", "agent_id");

-- +goose Down
DROP TABLE "tool_override";
