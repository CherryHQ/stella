-- +goose Up
-- lark-cli is now an ordinary user-managed CLI. Clean only the legacy built-in
-- override fields that could keep Stella OAuth injection alive; preserve enabled,
-- binary, skill, and unrelated prompt customizations.
UPDATE plugin_override
SET config = (
    CASE
        WHEN (config::jsonb ->> 'prompt') =
             'Always use lark-cli for Feishu/Lark operations (messages, docs, sheets, calendar, tasks, wiki, etc.). For authorization issues, use the `oauth` tool (oauth status, then oauth connect with provider=feishu) to reconnect — NEVER use `lark-cli auth`.'
        THEN config::jsonb - 'oauth_provider' - 'session_env' - 'prompt'
        ELSE config::jsonb - 'oauth_provider' - 'session_env'
    END
)::text,
updated_at = now()
WHERE plugin_id = 'tool/lark-cli'
  AND btrim(config) <> '';

-- +goose Down
-- Irreversible data cleanup: the removed OAuth binding may have contained
-- administrator customizations, so a Down migration must not invent them.
SELECT 1;
