-- +goose Up
-- lark-cli now owns its native per-user device authorization. Clean only the
-- legacy built-in override fields that could keep Stella OAuth injection alive;
-- preserve enabled, binary, skill, and unrelated prompt customizations.
UPDATE plugin_override
SET config = (
    CASE
        WHEN (config::jsonb ->> 'prompt') =
             'Always use lark-cli for Feishu/Lark operations (messages, docs, sheets, calendar, tasks, wiki, etc.). For authorization issues, use the `oauth` tool (oauth status, then oauth connect with provider=feishu) to reconnect — NEVER use `lark-cli auth`.'
        THEN jsonb_set(
            config::jsonb - 'oauth_provider' - 'session_env',
            '{prompt}',
            to_jsonb('Always use lark-cli for Feishu/Lark workspace operations. Stella preconfigures the current Agent''s Channel app for private user sessions. Use `lark-cli auth status` and lark-cli''s native device authorization when user access or additional scopes are required; use `--as user` for personal resources. Never use Stella''s oauth tool to authorize lark-cli.'::text),
            true
        )
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
