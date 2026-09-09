-- name: CreateSandboxGeneration :one
-- The caller takes LockSandboxGenerationSession in the same transaction before
-- this statement. Keeping the lock as a separate statement is required: a
-- waiting transaction must take a fresh READ COMMITTED snapshot after it
-- acquires the advisory lock before calculating MAX/NOT EXISTS.
WITH next_generation AS (
    SELECT COALESCE(MAX(generation), 0) + 1 AS generation
    FROM agent_sandbox_generation
    WHERE agent_sandbox_generation.session_id = sqlc.arg(session_id)
), inserted AS (
    INSERT INTO agent_sandbox_generation (
        session_id, generation, owner_boot_id, backend, config_digest, state
    )
    SELECT sqlc.arg(session_id), next_generation.generation, sqlc.arg(owner_boot_id),
           sqlc.arg(backend), sqlc.arg(config_digest), 'creating'
    FROM next_generation
    WHERE NOT EXISTS (
        SELECT 1
        FROM agent_sandbox_generation
        WHERE agent_sandbox_generation.session_id = sqlc.arg(session_id)
          AND agent_sandbox_generation.state <> 'destroyed'
    )
    RETURNING *
)
SELECT * FROM inserted;

-- name: LockSandboxGenerationSession :exec
SELECT pg_advisory_xact_lock(hashtextextended(sqlc.arg(session_id), 0));

-- name: GetSandboxGeneration :one
SELECT * FROM agent_sandbox_generation
WHERE session_id = sqlc.arg(session_id) AND generation = sqlc.arg(generation);

-- name: GetCurrentSandboxGeneration :one
SELECT * FROM agent_sandbox_generation
WHERE session_id = sqlc.arg(session_id)
ORDER BY generation DESC
LIMIT 1;

-- name: MarkSandboxGenerationActive :one
UPDATE agent_sandbox_generation
SET state = 'active', resource_authority = sqlc.arg(resource_authority),
    resource_ref = sqlc.arg(resource_ref), active_at = clock_timestamp(),
    last_error = '', updated_at = clock_timestamp()
WHERE session_id = sqlc.arg(session_id) AND generation = sqlc.arg(generation)
  AND owner_boot_id = sqlc.arg(owner_boot_id) AND state = 'creating'
RETURNING *;

-- name: MarkSandboxGenerationDestroyed :one
UPDATE agent_sandbox_generation
SET state = 'destroyed', destroyed_at = clock_timestamp(),
    last_error = sqlc.arg(last_error), updated_at = clock_timestamp()
WHERE agent_sandbox_generation.session_id = sqlc.arg(session_id)
  AND agent_sandbox_generation.generation = sqlc.arg(generation)
  AND agent_sandbox_generation.owner_boot_id = sqlc.arg(owner_boot_id)
  AND agent_sandbox_generation.state IN ('fenced', 'unknown')
  AND NOT EXISTS (
      SELECT 1 FROM agent_sandbox_generation_auxiliary a
      WHERE a.session_id = agent_sandbox_generation.session_id
        AND a.generation = agent_sandbox_generation.generation
        AND a.termination_state <> 'absent'
  )
RETURNING *;

-- name: MarkSandboxGenerationFenced :one
UPDATE agent_sandbox_generation
SET state = 'fenced', fenced_at = COALESCE(fenced_at, clock_timestamp()),
    last_error = sqlc.arg(last_error), updated_at = clock_timestamp()
WHERE session_id = sqlc.arg(session_id) AND generation = sqlc.arg(generation)
  AND state IN ('creating', 'active')
RETURNING *;

-- name: MarkSandboxGenerationUnknown :one
UPDATE agent_sandbox_generation
SET state = 'unknown', fenced_at = COALESCE(fenced_at, clock_timestamp()),
    last_error = sqlc.arg(last_error), updated_at = clock_timestamp()
WHERE session_id = sqlc.arg(session_id) AND generation = sqlc.arg(generation)
  AND state IN ('creating', 'active', 'fenced')
RETURNING *;

-- name: MarkSandboxGenerationReconciledAbsent :one
UPDATE agent_sandbox_generation
SET state = 'destroyed', destroyed_at = clock_timestamp(),
    last_error = sqlc.arg(last_error), updated_at = clock_timestamp()
WHERE agent_sandbox_generation.session_id = sqlc.arg(session_id)
  AND agent_sandbox_generation.generation = sqlc.arg(generation)
  AND agent_sandbox_generation.state IN ('fenced', 'unknown')
  AND NOT EXISTS (
      SELECT 1 FROM agent_sandbox_generation_auxiliary a
      WHERE a.session_id = agent_sandbox_generation.session_id
        AND a.generation = agent_sandbox_generation.generation
        AND a.termination_state <> 'absent'
  )
RETURNING *;

-- name: AcknowledgeSandboxGenerationAbsent :one
-- Manual absence is intentionally narrow. The owner boot must be drained or
-- stale, and no running AgentRun may still be able to issue a backend call.
UPDATE agent_sandbox_generation g
SET state = 'destroyed', destroyed_at = clock_timestamp(),
    last_error = sqlc.arg(reason), updated_at = clock_timestamp()
WHERE g.session_id = sqlc.arg(session_id)
  AND g.generation = sqlc.arg(generation)
  AND g.owner_boot_id = sqlc.arg(owner_boot_id)
  AND g.state = 'unknown'
  AND (sqlc.arg(force)::boolean)
  AND (
      EXISTS (
          SELECT 1 FROM runtime_executor_boot b
          WHERE b.id = g.owner_boot_id
            AND (b.status = 'drained' OR b.heartbeat_at < clock_timestamp() - interval '30 seconds')
      )
  )
  AND NOT EXISTS (
      SELECT 1 FROM agent_run r
      WHERE r.session_id = g.session_id AND r.status = 'running'
  )
RETURNING g.*;

-- name: CreateSandboxGenerationAuxiliary :one
INSERT INTO agent_sandbox_generation_auxiliary (
    aux_id, session_id, generation, role, backend, backing_root
)
SELECT sqlc.arg(aux_id), p.session_id, p.generation,
       sqlc.arg(role), sqlc.arg(backend), sqlc.arg(backing_root)
FROM agent_sandbox_generation p
WHERE p.session_id = sqlc.arg(session_id)
  AND p.generation = sqlc.arg(generation)
  AND p.owner_boot_id = sqlc.arg(owner_boot_id)
  AND p.state IN ('creating', 'active')
  AND (
      SELECT count(*)
      FROM agent_sandbox_generation_auxiliary a
      WHERE a.session_id = p.session_id
        AND a.generation = p.generation
        AND a.termination_state <> 'absent'
  ) < sqlc.arg(max_auxiliaries)::bigint
RETURNING *;

-- name: LockSandboxGenerationAuxiliaryParent :one
SELECT session_id, generation
FROM agent_sandbox_generation
WHERE session_id = sqlc.arg(session_id)
  AND generation = sqlc.arg(generation)
  AND owner_boot_id = sqlc.arg(owner_boot_id)
  AND state IN ('creating', 'active')
FOR UPDATE;

-- name: CountSandboxGenerationOpenAuxiliaries :one
SELECT count(*)::bigint AS count
FROM agent_sandbox_generation_auxiliary
WHERE session_id = sqlc.arg(session_id)
  AND generation = sqlc.arg(generation)
  AND termination_state <> 'absent';

-- name: GetSandboxGenerationAuxiliary :one
SELECT * FROM agent_sandbox_generation_auxiliary
WHERE aux_id = sqlc.arg(aux_id);

-- name: ListSandboxGenerationAuxiliaries :many
SELECT * FROM agent_sandbox_generation_auxiliary
WHERE session_id = sqlc.arg(session_id) AND generation = sqlc.arg(generation)
ORDER BY created_at, aux_id;

-- name: MarkSandboxGenerationAuxiliaryIdentity :one
UPDATE agent_sandbox_generation_auxiliary
SET resource_authority = sqlc.arg(resource_authority),
    resource_ref = sqlc.arg(resource_ref),
    updated_at = clock_timestamp()
WHERE aux_id = sqlc.arg(aux_id)
  AND session_id = sqlc.arg(session_id)
  AND generation = sqlc.arg(generation)
  AND execution_state = 'creating'
RETURNING *;

-- name: MarkSandboxGenerationAuxiliaryExecution :one
UPDATE agent_sandbox_generation_auxiliary
SET execution_state = sqlc.arg(execution_state),
    last_error = sqlc.arg(last_error),
    updated_at = clock_timestamp()
WHERE aux_id = sqlc.arg(aux_id)
  AND (
      execution_state = sqlc.arg(execution_state)
      OR (execution_state = 'creating' AND sqlc.arg(execution_state) IN ('succeeded', 'failed', 'unknown'))
  )
RETURNING *;

-- name: MarkSandboxGenerationAuxiliaryTermination :one
UPDATE agent_sandbox_generation_auxiliary
SET termination_state = sqlc.arg(termination_state),
    last_error = sqlc.arg(last_error),
    updated_at = clock_timestamp()
WHERE aux_id = sqlc.arg(aux_id)
  AND (
      termination_state <> 'absent'
      OR sqlc.arg(termination_state) = 'absent'
  )
RETURNING *;

-- name: AcknowledgeSandboxGenerationAuxiliaries :exec
UPDATE agent_sandbox_generation_auxiliary
SET termination_state = 'absent',
    last_error = sqlc.arg(last_error),
    updated_at = clock_timestamp()
WHERE session_id = sqlc.arg(session_id)
  AND generation = sqlc.arg(generation)
  AND termination_state <> 'absent';
