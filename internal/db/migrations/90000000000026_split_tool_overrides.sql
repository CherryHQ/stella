-- +goose Up
-- The seven union tools became one tool per action, and four recally tools were
-- renamed. A tool_override row is keyed by name, so every row written against a
-- retired name now matches nothing: it neither hides its old capability nor any
-- of the new ones, it just sits there until some future tool reuses the name and
-- silently inherits the setting.
--
-- Stella is pre-production: there are no tool_override rows to preserve, so this
-- is a clean break rather than the expand-then-contract dance. Delete the rows
-- that name a retired tool. The capability returns to its default visibility,
-- which is what an operator who never set an override would already have.
--
-- The pre-#1171 "recally" union is not in this list: it was retired by #1171,
-- not by this change.
DELETE FROM tool_override
WHERE tool_name IN (
    'goal',
    'scheduler',
    'workflow',
    'vault',
    'oauth',
    'share',
    'email',
    'recally_get_article',
    'recally_list_articles',
    'recally_save_article',
    'recally_digest'
);

-- +goose Down
-- Deliberately a no-op. Up deletes rows; a Down cannot invent the enabled flag,
-- the scope, or the owner they carried, and guessing them would hand a user back
-- a capability they had switched off — or take one away. Rolling this migration
-- back leaves the retired names absent, which is the same state a deployment
-- that never had an override is in.
SELECT 1;
