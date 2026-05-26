-- name: GetAuthOAuthProvider :one
SELECT id, provider_id, client_id, client_secret_enc, redirect_url, created_at, updated_at
FROM plugin_oauth_provider
WHERE provider_id = ?;

-- name: UpsertAuthOAuthProvider :exec
INSERT INTO plugin_oauth_provider (id, provider_id, client_id, client_secret_enc, redirect_url)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(provider_id) DO UPDATE SET
    client_id         = excluded.client_id,
    client_secret_enc = excluded.client_secret_enc,
    redirect_url      = excluded.redirect_url,
    updated_at        = datetime('now');

-- name: DeleteAuthOAuthProvider :exec
DELETE FROM plugin_oauth_provider WHERE provider_id = ?;
