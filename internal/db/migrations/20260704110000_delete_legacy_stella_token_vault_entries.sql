-- +goose Up
DELETE FROM vault_entry
WHERE name = 'STELLA_TOKEN';

-- +goose Down
-- Irreversible: deleted secret plaintext cannot be reconstructed.
SELECT 1;
