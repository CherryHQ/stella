-- name: GetOAuthProviderConfig :one
SELECT provider_id, client_id, client_secret, redirect_url, updated_at
FROM oauth_provider_configs
WHERE provider_id = ?;

-- name: UpsertOAuthProviderConfig :exec
INSERT INTO oauth_provider_configs (provider_id, client_id, client_secret, redirect_url)
VALUES (?, ?, ?, ?)
ON CONFLICT(provider_id) DO UPDATE SET
    client_id     = excluded.client_id,
    client_secret = excluded.client_secret,
    redirect_url  = excluded.redirect_url,
    updated_at    = datetime('now');
