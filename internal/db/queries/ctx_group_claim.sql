-- name: ClaimGroupWork :one
INSERT INTO ctx_group_claim (id, group_id, key, owner_agent_id, note, lease_until)
VALUES (sqlc.arg(id), sqlc.arg(group_id), sqlc.arg(key), sqlc.arg(owner_agent_id), sqlc.arg(note), sqlc.arg(lease_until))
ON CONFLICT (group_id, key) DO UPDATE
SET owner_agent_id = EXCLUDED.owner_agent_id,
    note = EXCLUDED.note,
    lease_until = EXCLUDED.lease_until,
    updated_at = now()
WHERE ctx_group_claim.lease_until < now()
   OR ctx_group_claim.owner_agent_id = EXCLUDED.owner_agent_id
RETURNING *;

-- name: GetLiveGroupClaim :one
SELECT * FROM ctx_group_claim
WHERE group_id = sqlc.arg(group_id)
  AND key = sqlc.arg(key)
  AND lease_until > now();

-- name: ListLiveGroupClaims :many
SELECT * FROM ctx_group_claim
WHERE group_id = sqlc.arg(group_id)
  AND lease_until > now()
ORDER BY created_at ASC, key ASC;

-- name: DeleteExpiredGroupClaims :execrows
-- Claims are read-side filtered by `lease_until > now()`, so an expired row is
-- already invisible; without this the table only ever shrinks on group delete.
-- The grace period keeps a just-expired claim readable for a while: an agent
-- that overran its lease still sees its own note when it comes back, and an
-- operator debugging a stuck turn can still see who held what. One hour is
-- comfortably longer than any single turn.
DELETE FROM ctx_group_claim
WHERE lease_until < sqlc.arg('now')::timestamptz - interval '1 hour';

-- name: ReleaseGroupClaim :execrows
DELETE FROM ctx_group_claim
WHERE group_id = sqlc.arg(group_id)
  AND key = sqlc.arg(key)
  AND owner_agent_id = sqlc.arg(owner_agent_id);
