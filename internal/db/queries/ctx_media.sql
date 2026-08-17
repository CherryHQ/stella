-- name: CreateMediaIfAbsent :one
-- A conflict returns no rows. Callers must issue GetMediaByUserAndSHA256 in a
-- separate statement, then verify immutable metadata before reusing the row.
INSERT INTO ctx_media (user_id, sha256, mime_type, size_bytes)
VALUES ($1, $2, $3, $4)
ON CONFLICT (user_id, sha256) DO NOTHING
RETURNING *;

-- name: GetMediaByUserAndSHA256 :one
SELECT * FROM ctx_media
WHERE user_id = $1 AND sha256 = $2;

-- name: ListMediaByIDsForUser :many
-- Canonical append validates every media reference against this user inside its
-- parent/parts transaction. Callers deduplicate IDs before this batch lookup.
SELECT * FROM ctx_media
WHERE user_id = sqlc.arg('user_id')
  AND id = ANY(sqlc.arg('media_ids')::uuid[])
ORDER BY id ASC;

-- name: ListMediaByIDs :many
-- Internal durable-queue reconstruction starts from already-authorized media
-- references stored in the event log. IDs are globally unique, so no mutable
-- owner/session lookup is needed to recover immutable size metadata.
SELECT * FROM ctx_media
WHERE id = ANY(sqlc.arg('media_ids')::uuid[])
ORDER BY id ASC;

-- name: GetMediaForSession :one
-- Authorize immutable media through an ordinary session message part. The
-- conversation's legacy text owner is intentionally compared to the UUID media
-- owner so group conversations cannot resolve user media.
SELECT m.*
FROM ctx_media m
WHERE m.id = sqlc.arg(media_id)
  AND m.user_id = sqlc.arg(user_id)
  AND EXISTS (
      SELECT 1
      FROM ctx_message_part p
      JOIN ctx_message msg ON msg.id = p.message_id
      JOIN ctx_conversation c ON c.id = msg.conversation_id
      WHERE p.media_id = m.id
        AND c.session_id = sqlc.arg(session_id)
        AND c.user_id = m.user_id::text
        AND c.agent_id IS NOT DISTINCT FROM sqlc.narg(agent_id)
  );
