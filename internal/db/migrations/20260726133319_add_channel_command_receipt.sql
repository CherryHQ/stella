-- +goose Up
-- Records that one inbound group message's slash command has already been
-- executed, so a platform redelivery (or a Web retry with the same
-- client_message_id) answers instead of executing it a second time.
--
-- Ordinary group messages get this for free: they are written to
-- ctx_group_message, whose (group_id, platform_message_id) unique index makes
-- the append itself the dedup. `/new` cannot use that path — it must be
-- intercepted BEFORE the append, because its whole purpose is to clear the
-- context the append would put it in, and ctx_group_message has no way to carry
-- a row that its four readers (LCM assembly, the semantic arbiter, group-memory
-- ingest, the Web history API) all agree to skip. So the receipt lives here, on
-- the same insert-based dedup shape as email_send_dedup.
--
-- A receipt is claimed before the command runs, not after: re-running a
-- destructive reset would silently discard whatever the group said in between,
-- while a lost reset is one message away from being asked for again.
CREATE TABLE channel_group_command_receipt (
    id         UUID PRIMARY KEY DEFAULT uuidv7(),
    group_id   UUID NOT NULL REFERENCES ctx_group_state(id) ON DELETE CASCADE,
    -- Origin of the message id, so a platform id can never collide with a Web
    -- client_message_id for the same group.
    platform   TEXT NOT NULL,
    -- Platform message id, or the Web client_message_id. Never empty: a message
    -- with no id cannot be recognised on redelivery, so the caller refuses to
    -- run the destructive command at all rather than storing a blank that
    -- would collapse every such message onto one row.
    message_id TEXT NOT NULL,
    command    TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (group_id, platform, message_id)
);

-- +goose Down
DROP TABLE channel_group_command_receipt;
