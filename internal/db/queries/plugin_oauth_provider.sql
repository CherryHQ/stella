-- name: GetAuthOAuthProvider :one
SELECT id, provider_id, client_id, client_secret_enc, redirect_url, created_at, updated_at
FROM plugin_oauth_provider
WHERE provider_id = $1;

-- name: UpsertAuthOAuthProvider :exec
INSERT INTO plugin_oauth_provider (id, provider_id, client_id, client_secret_enc, redirect_url)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT(provider_id) DO UPDATE SET
    client_id         = excluded.client_id,
    client_secret_enc = excluded.client_secret_enc,
    redirect_url      = excluded.redirect_url,
    updated_at        = now();

-- name: DeleteAuthOAuthProvider :exec
DELETE FROM plugin_oauth_provider WHERE provider_id = $1;
