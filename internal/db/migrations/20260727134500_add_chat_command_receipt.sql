-- +goose Up
-- The DM sibling of channel_group_command_receipt: records that one inbound
-- private-chat message's slash command has already been executed, so a platform
-- redelivery answers instead of executing it a second time. DMs never had an
-- equivalent of the group event log's (group_id, platform_message_id) dedup, so
-- a redelivered `/new` would re-resolve the successor session and rotate it
-- again — silently archiving everything said since.
--
-- The unique key is the message's PHYSICAL delivery coordinates: which channel
-- instance delivered it, which platform chat it arrived in, and its platform
-- message id. Routing state (which agent, which session binding) is
-- deliberately excluded: `/agent` or link changes alter routing, and the same
-- physical message redelivered after such a change is still the same message —
-- keying on routing would let it execute twice. For the same reason there is
-- no FK to agent: deleting an agent must not release receipts for messages
-- that already executed. `binding` is audit-only.
--
-- Claimed before the command runs, released only on provable non-execution,
-- and never expired — the same contract as the group receipt.
CREATE TABLE channel_chat_command_receipt (
    id         UUID PRIMARY KEY DEFAULT uuidv7(),
    -- Configured channel instance id (defaults to the platform name). Platform
    -- message ids are only unique within one channel instance — two bots on the
    -- same platform can both count message ids from 1 — so the instance, not
    -- the platform, is the id's namespace.
    channel_id TEXT NOT NULL,
    -- The physical chat the message arrived in: the platform chat id, or the
    -- sender's platform id for platforms that leave ChatID empty in DMs. One
    -- linked Stella user can own several platform accounts on one bot, and
    -- message ids are only unique per chat, so the chat must be part of the
    -- identity.
    chat_key   TEXT NOT NULL,
    -- Platform message id. Never empty: a message with no id cannot be
    -- recognised on redelivery, so the caller skips the receipt entirely rather
    -- than storing a blank that would collapse every such message onto one row.
    message_id TEXT NOT NULL,
    command    TEXT NOT NULL,
    -- The chat binding the command executed under, for auditing only.
    binding    TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (channel_id, chat_key, message_id)
);

-- +goose Down
DROP TABLE channel_chat_command_receipt;
