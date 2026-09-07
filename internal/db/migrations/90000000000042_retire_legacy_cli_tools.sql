-- +goose Up

-- All statements in this migration run in Goose's transaction. These
-- checks must precede every DELETE so an unexpected/custom row rolls back the
-- whole migration and remains inspectable.
-- +goose StatementBegin
DO $$
DECLARE
    bad text;
BEGIN
    -- Legacy plugin rows must still be the shipped tool identity. A malformed
    -- config is not silently discarded with the row.
    SELECT string_agg(format('%s(kind=%s,name=%s)', id, kind, name), ', ' ORDER BY id)
      INTO bad
      FROM plugin
     WHERE id IN ('tool/mise', 'tool/xberg', 'tool/fd', 'tool/rg')
       AND (kind <> 'tool'
            OR name <> split_part(id, '/', 2)
            OR jsonb_typeof(config) <> 'object');
    IF bad IS NOT NULL THEN
        RAISE EXCEPTION 'retired builtin plugin has unexpected legacy shape: %', bad;
    END IF;

    -- Likewise, the old manifest row may point at a vault-backed session-env
    -- blob. It cannot be moved by this schema-only cleanup.
    IF EXISTS (
        SELECT 1
          FROM plugin_override
         WHERE plugin_id IN ('tool/mise', 'tool/xberg', 'tool/fd', 'tool/rg')
           AND session_env_vault_key <> ''
    ) THEN
        RAISE EXCEPTION 'retired builtin manifest override contains a vault locator';
    END IF;

    -- Non-empty manifest overrides must at least be readable JSON objects.
    -- The cast deliberately aborts the Goose transaction on malformed JSON.
    IF EXISTS (
        SELECT 1 FROM plugin_override
         WHERE plugin_id IN ('tool/mise', 'tool/xberg', 'tool/fd', 'tool/rg')
           AND config <> ''
    ) THEN
        PERFORM config::jsonb
          FROM plugin_override
         WHERE plugin_id IN ('tool/mise', 'tool/xberg', 'tool/fd', 'tool/rg')
           AND config <> ''
           AND jsonb_typeof(config::jsonb) <> 'object';
        IF FOUND THEN
            RAISE EXCEPTION 'retired builtin manifest override is not a JSON object';
        END IF;
    END IF;
END
$$;
-- +goose StatementEnd

-- Retire only rows owned by the old plugin tables. The unified catalog and
-- its agent-scoped configuration use bare IDs and remain authoritative.
DELETE FROM plugin_state
 WHERE plugin_id IN ('tool/mise', 'tool/xberg', 'tool/fd', 'tool/rg');
DELETE FROM plugin_override
 WHERE plugin_id IN ('tool/mise', 'tool/xberg', 'tool/fd', 'tool/rg');
DELETE FROM plugin
 WHERE id IN ('tool/mise', 'tool/xberg', 'tool/fd', 'tool/rg');

-- +goose Down
-- Cleanup is forward-only and does not reconstruct
-- release-owned definitions or user policy.
-- +goose StatementBegin
DO $$
BEGIN
    RAISE EXCEPTION 'migration 90000000000042 is irreversible after retirement';
END
$$;
-- +goose StatementEnd
