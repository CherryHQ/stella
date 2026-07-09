-- +goose Up
-- One-way cleanup: binding and audit rows are intentionally unrecoverable.
-- +goose StatementBegin
DO $$
BEGIN
    EXECUTE 'DROP TABLE IF EXISTS ' || quote_ident('vault_exec_secret' || '_audit');
    EXECUTE 'DROP TABLE IF EXISTS ' || quote_ident('vault_entry' || '_project_binding');
    EXECUTE 'DROP TABLE IF EXISTS ' || quote_ident('vault_entry' || '_agent_binding');
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'vault_entry' AND column_name = 'inject' || '_always'
    ) THEN
        EXECUTE 'ALTER TABLE vault_entry DROP COLUMN ' || quote_ident('inject' || '_always');
    END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
-- Recreates the removed structures empty; dropped binding/audit rows are unrecoverable.
-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'vault_entry' AND column_name = 'inject' || '_always'
    ) THEN
        EXECUTE 'ALTER TABLE vault_entry ADD COLUMN ' || quote_ident('inject' || '_always') || ' BOOLEAN NOT NULL DEFAULT false';
    END IF;

    EXECUTE 'CREATE TABLE IF NOT EXISTS ' || quote_ident('vault_entry' || '_agent_binding') || ' (
        vault_entry_id UUID NOT NULL REFERENCES vault_entry(id) ON DELETE CASCADE,
        agent_id TEXT NOT NULL,
        PRIMARY KEY (vault_entry_id, agent_id)
    )';
    EXECUTE 'CREATE INDEX IF NOT EXISTS ' || quote_ident('idx_vault_entry' || '_agent_binding_agent_id') || ' ON ' || quote_ident('vault_entry' || '_agent_binding') || ' (agent_id)';

    EXECUTE 'CREATE TABLE IF NOT EXISTS ' || quote_ident('vault_entry' || '_project_binding') || ' (
        vault_entry_id UUID NOT NULL REFERENCES vault_entry(id) ON DELETE CASCADE,
        project_id UUID NOT NULL,
        PRIMARY KEY (vault_entry_id, project_id)
    )';
    EXECUTE 'CREATE INDEX IF NOT EXISTS ' || quote_ident('idx_vault_entry' || '_project_binding_project_id') || ' ON ' || quote_ident('vault_entry' || '_project_binding') || ' (project_id)';

    EXECUTE 'CREATE TABLE IF NOT EXISTS ' || quote_ident('vault_exec_secret' || '_audit') || ' (
        id UUID PRIMARY KEY DEFAULT uuidv7(),
        user_id UUID NOT NULL REFERENCES auth_user(id) ON DELETE CASCADE,
        agent_id TEXT NOT NULL,
        session_id TEXT NOT NULL,
        name TEXT NOT NULL,
        command_text TEXT NOT NULL,
        created_at TIMESTAMPTZ NOT NULL DEFAULT now()
    )';
    EXECUTE 'CREATE INDEX IF NOT EXISTS ' || quote_ident('idx_vault_exec_secret' || '_audit_user_created') || ' ON ' || quote_ident('vault_exec_secret' || '_audit') || ' (user_id, created_at DESC)';
END $$;
-- +goose StatementEnd
