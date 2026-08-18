-- +goose Up
-- Wake rows are inert to a Phase-1 binary, which polls reply rows only. Convert
-- live leases too: an interrupted release must resume the same durable work
-- after expiry rather than strand it behind the removed reply feed.
UPDATE ctx_group_dispatch
SET kind = 'wake', updated_at = now()
WHERE kind = 'reply';

ALTER TABLE ctx_group_dispatch
    ALTER COLUMN kind SET DEFAULT 'wake';

-- +goose Down
-- Phase-1 deliberately ignores wake work on rollback. Rewriting it as reply
-- would replay optimistic backlog through the arbiter, so this is irreversible.
SELECT 1;
