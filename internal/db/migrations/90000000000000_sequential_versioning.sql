-- This no-op anchors Goose's sequential creator after the immutable timestamp
-- history. It is deliberately 14 digits and lexically after 2-prefixed legacy
-- files, so Goose and sqlc use the same ordering.
-- +goose Up
SELECT 1;

-- This transition changes no schema, so rollback has nothing to reverse; keep
-- the explicit no-op to satisfy Goose's complete Up/Down migration contract.
-- +goose Down
SELECT 1;
