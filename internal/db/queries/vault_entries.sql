-- name: ListVaultEntriesByUser :many
SELECT id, user_id, name, ciphertext, created_at, updated_at
FROM vault_entries
WHERE user_id = ?
ORDER BY name;

-- name: GetVaultEntry :one
SELECT id, user_id, name, ciphertext, created_at, updated_at
FROM vault_entries
WHERE user_id = ? AND name = ?;

-- name: UpsertVaultEntry :exec
INSERT INTO vault_entries (id, user_id, name, ciphertext)
VALUES (?, ?, ?, ?)
ON CONFLICT(user_id, name) DO UPDATE SET
    ciphertext = excluded.ciphertext,
    updated_at = datetime('now');

-- name: DeleteVaultEntry :exec
DELETE FROM vault_entries WHERE user_id = ? AND name = ?;

-- name: DeleteAllVaultEntriesByUser :exec
DELETE FROM vault_entries WHERE user_id = ?;
