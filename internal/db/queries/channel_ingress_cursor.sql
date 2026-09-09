-- name: EnsureChannelIngressCursor :one
INSERT INTO channel_ingress_cursor (
    id, channel_id, platform, stream_key, cursor
)
VALUES (
    sqlc.arg(id)::uuid,
    sqlc.arg(channel_id),
    sqlc.arg(platform),
    sqlc.arg(stream_key),
    sqlc.arg(cursor)::bigint
)
ON CONFLICT (channel_id, platform, stream_key) DO UPDATE
SET updated_at = now()
RETURNING *;

-- name: SaveChannelIngressSessionState :one
INSERT INTO channel_ingress_cursor (
    id, channel_id, platform, stream_key, cursor,
    session_id, resume_gateway_url, session_state, last_error
)
VALUES (
    sqlc.arg(id)::uuid,
    sqlc.arg(channel_id),
    sqlc.arg(platform),
    sqlc.arg(stream_key),
    sqlc.arg(cursor)::bigint,
    sqlc.arg(session_id),
    sqlc.arg(resume_gateway_url),
    sqlc.arg(session_state),
    sqlc.arg(last_error)
)
ON CONFLICT (channel_id, platform, stream_key) DO UPDATE
SET cursor = GREATEST(channel_ingress_cursor.cursor, EXCLUDED.cursor),
    session_id = EXCLUDED.session_id,
    resume_gateway_url = EXCLUDED.resume_gateway_url,
    session_state = EXCLUDED.session_state,
    last_error = EXCLUDED.last_error,
    updated_at = now()
RETURNING *;

-- name: AdvanceChannelIngressCursor :one
INSERT INTO channel_ingress_cursor (
    id, channel_id, platform, stream_key, cursor
)
VALUES (
    sqlc.arg(id)::uuid,
    sqlc.arg(channel_id),
    sqlc.arg(platform),
    sqlc.arg(stream_key),
    sqlc.arg(cursor)::bigint
)
ON CONFLICT (channel_id, platform, stream_key) DO UPDATE
SET cursor = GREATEST(channel_ingress_cursor.cursor, EXCLUDED.cursor),
    updated_at = now()
RETURNING *;
