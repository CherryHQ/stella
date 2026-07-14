-- +goose Up
-- A bidirectional channel is a single binding for one agent and platform. Legacy
-- rows used an empty type to mean the channel ID; normalize that representation
-- before enforcing the invariant. If that exposes existing duplicates, CREATE
-- INDEX fails atomically and preserves every row for an operator to resolve.
UPDATE channel
SET type = id
WHERE type = '';

CREATE UNIQUE INDEX idx_channel_agent_id_type
ON channel (agent_id, type)
WHERE agent_id IS NOT NULL
  AND agent_id <> ''
  AND type <> 'webhook';

-- +goose Down
DROP INDEX IF EXISTS idx_channel_agent_id_type;
