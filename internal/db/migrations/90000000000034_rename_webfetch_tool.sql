-- +goose Up
-- `webfetch` is being renamed to the generated `web_fetch` name. A
-- tool_override row is keyed by name, so a row for the retired name would
-- otherwise become inert and could be inherited if that name is reused.
--
-- Stella is pre-production, so this is a clean break. Deleting the old row
-- restores the new tool's default visibility without guessing whether an old
-- allow or deny should transfer.
DELETE FROM tool_override
WHERE tool_name = 'webfetch';

-- +goose Down
-- Deliberately a no-op: the deleted override's scope, owner, and enabled value
-- cannot be reconstructed safely.
SELECT 1;
