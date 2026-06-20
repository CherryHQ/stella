-- name: GetUserAgentMemory :one
SELECT * FROM ctx_agent_memory WHERE user_id = $1 AND agent_id = $2;

-- name: UpsertUserAgentMemory :exec
INSERT INTO ctx_agent_memory (user_id, agent_id, content, updated_at)
VALUES ($1, $2, $3, now())
ON CONFLICT(user_id, agent_id) DO UPDATE SET
    content = excluded.content,
    updated_at = now();

-- name: UpsertAgentSoul :exec
INSERT INTO ctx_agent_memory (user_id, agent_id, soul, updated_at)
VALUES ($1, $2, $3, now())
ON CONFLICT(user_id, agent_id) DO UPDATE SET
    soul = excluded.soul,
    updated_at = now();

-- name: DeleteUserAgentMemory :exec
DELETE FROM ctx_agent_memory WHERE user_id = $1 AND agent_id = $2;

-- name: ListUserAgentMemoriesByUser :many
SELECT * FROM ctx_agent_memory WHERE user_id = $1 ORDER BY agent_id;

-- name: UpsertUserAgentMemoryVersioned :one
INSERT INTO ctx_agent_memory (user_id, agent_id, content, version, updated_at)
VALUES ($1, $2, $3, 1, now())
ON CONFLICT(user_id, agent_id) DO UPDATE SET
    content = excluded.content,
    version = ctx_agent_memory.version + 1,
    updated_at = now()
RETURNING *;

-- name: UpsertAgentConstraints :one
INSERT INTO ctx_agent_memory (user_id, agent_id, constraints, version, updated_at)
VALUES ($1, $2, $3, 1, now())
ON CONFLICT(user_id, agent_id) DO UPDATE SET
    constraints = excluded.constraints,
    version = ctx_agent_memory.version + 1,
    updated_at = now()
RETURNING *;

-- name: UpsertAgentSoulVersioned :one
INSERT INTO ctx_agent_memory (user_id, agent_id, soul, version, updated_at)
VALUES ($1, $2, $3, 1, now())
ON CONFLICT(user_id, agent_id) DO UPDATE SET
    soul = excluded.soul,
    version = ctx_agent_memory.version + 1,
    updated_at = now()
RETURNING *;

-- name: UpsertProfileEntries :one
INSERT INTO ctx_agent_memory (user_id, agent_id, profile_entries, version, updated_at)
VALUES ($1, $2, $3, 1, now())
ON CONFLICT(user_id, agent_id) DO UPDATE SET
    profile_entries = excluded.profile_entries,
    version = ctx_agent_memory.version + 1,
    updated_at = now()
RETURNING *;
