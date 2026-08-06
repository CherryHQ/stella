-- +goose Up
-- A constant default is metadata-only on supported PostgreSQL versions, so
-- existing rows remain personal without a table rewrite. Bound the brief
-- ALTER TABLE lock wait rather than queueing behind production traffic.
SET LOCAL lock_timeout = '5s';
ALTER TABLE personal_access_token
    ADD COLUMN token_use TEXT NOT NULL DEFAULT 'personal';

-- +goose Down
SET LOCAL lock_timeout = '5s';
ALTER TABLE personal_access_token
    DROP COLUMN token_use;
