-- name: GetModelCatalog :one
SELECT * FROM model_catalog WHERE id = 'models.dev';

-- name: UpsertModelCatalog :exec
INSERT INTO model_catalog (id, payload, etag, synced_at, updated_at)
VALUES ('models.dev', $1, $2, $3, now())
ON CONFLICT (id) DO UPDATE SET
    payload = EXCLUDED.payload,
    etag = EXCLUDED.etag,
    synced_at = EXCLUDED.synced_at,
    updated_at = now();
