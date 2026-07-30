-- +goose Up
-- A pending, single-use permission to rotate one chat onto a fresh session.
-- The `session_control` tool issues a row when the agent asks the user to
-- confirm a reset and consumes it when the user agrees in a later turn, so the
-- two halves of the confirmation must survive a restart and be visible to every
-- node. Rows are short-lived: consumed on success, swept on expiry.
CREATE TABLE agent_session_rotation_nonce (
    id          UUID PRIMARY KEY DEFAULT uuidv7(),
    session_id  TEXT NOT NULL REFERENCES ctx_conversation(session_id) ON DELETE CASCADE,
    -- Opaque identity of the durable chat binding (main / channel / group) the
    -- nonce was issued for; compared verbatim so the schema never has to track
    -- how a binding is shaped.
    binding_key TEXT NOT NULL,
    -- The speaker who asked for the reset. Empty for a DM, where the session
    -- owner is the only possible actor.
    actor_id    TEXT NOT NULL DEFAULT '',
    -- Marker of the turn that issued the nonce. Confirming from the same turn
    -- means no user ever answered, so the marker must differ to consume it.
    turn_marker TEXT NOT NULL,
    expires_at  TIMESTAMPTZ NOT NULL,
    used_at     TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_agent_session_rotation_nonce_session_id ON agent_session_rotation_nonce (session_id);
CREATE INDEX idx_agent_session_rotation_nonce_expires_at ON agent_session_rotation_nonce (expires_at);

-- +goose Down
DROP TABLE agent_session_rotation_nonce;
