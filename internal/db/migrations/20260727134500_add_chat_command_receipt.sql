-- +goose Up
-- The DM sibling of channel_group_command_receipt: records that one inbound
-- private-chat message's slash command has already been executed, so a platform
-- redelivery answers instead of executing it a second time. DMs never had an
-- equivalent of the group event log's (group_id, platform_message_id) dedup, so
-- a redelivered `/new` would re-resolve the successor session and rotate it
-- again — silently archiving everything said since.
--
-- Claimed before the command runs, released only on provable non-execution,
-- and never expired — the same contract as the group receipt.
CREATE TABLE channel_chat_command_receipt (
    id         UUID PRIMARY KEY DEFAULT uuidv7(),
    agent_id   TEXT NOT NULL REFERENCES agent(id) ON DELETE CASCADE,
    -- The chat's durable queue/binding key (linked user: agent + auth user id;
    -- unlinked: the full derived session key). Message ids are only meaningful
    -- within one chat, so the claim is scoped to it.
    binding    TEXT NOT NULL,
    -- Configured channel instance id (defaults to the platform name). Platform
    -- message ids are only unique within one channel instance — two bots on the
    -- same platform can both count message ids from 1 — so the instance, not
    -- the platform, is the id's namespace.
    channel_id TEXT NOT NULL,
    -- Platform message id. Never empty: a message with no id cannot be
    -- recognised on redelivery, so the caller skips the receipt entirely rather
    -- than storing a blank that would collapse every such message onto one row.
    message_id TEXT NOT NULL,
    command    TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (binding, channel_id, message_id)
);

-- +goose Down
DROP TABLE channel_chat_command_receipt;
