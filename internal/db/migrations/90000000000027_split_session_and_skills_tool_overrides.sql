-- +goose Up
-- The `session` union became four action tools and the `skills` union became
-- two. A tool_override row is keyed by name, so a row written against either
-- retired name now matches nothing: it neither hides its old capability nor any
-- of the new ones, it just sits there until some future tool reuses the name and
-- silently inherits the setting.
--
-- Same clean break as 90000000000026 (rules/agent-tools.md §10): Stella is
-- pre-production, there are no tool_override rows to preserve, so delete the
-- rows naming a retired tool. The capability returns to its default visibility,
-- which is what an operator who never set an override would already have.
DELETE FROM tool_override
WHERE tool_name IN ('session', 'skills');

-- +goose Down
-- Deliberately a no-op, for the same reason as 90000000000026: Up deletes rows,
-- and a Down cannot invent the enabled flag, the scope, or the owner they
-- carried. Guessing them would hand a user back a capability they had switched
-- off — or take one away.
SELECT 1;
