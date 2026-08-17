-- name: GetSessionSandbox :one
SELECT * FROM agent_session_sandbox WHERE session_id = $1;

-- name: CreateSessionSandboxGeneration :one
INSERT INTO agent_session_sandbox (
    session_id, generation, state, executor_boot_id, run_id, resource_backend, resource_id
)
VALUES (
    sqlc.arg(session_id), 1, 'creating', sqlc.arg(executor_boot_id),
    sqlc.arg(run_id), sqlc.arg(resource_backend), sqlc.arg(resource_id)
)
ON CONFLICT (session_id) DO UPDATE
SET generation = agent_session_sandbox.generation + 1,
    state = 'creating', executor_boot_id = excluded.executor_boot_id,
    run_id = excluded.run_id,
    resource_backend = excluded.resource_backend, resource_id = excluded.resource_id,
    fenced_at = NULL, destroyed_at = NULL, updated_at = now()
WHERE agent_session_sandbox.state = 'absent'
   OR (agent_session_sandbox.state = 'destroyed' AND agent_session_sandbox.destroyed_at IS NOT NULL)
RETURNING *;

-- name: ActivateSessionSandboxGeneration :execrows
UPDATE agent_session_sandbox
SET state = 'active', updated_at = now()
WHERE session_id = sqlc.arg(session_id)
  AND generation = sqlc.arg(generation)
  AND executor_boot_id = sqlc.arg(executor_boot_id)
  AND run_id = sqlc.arg(run_id)
  AND state = 'creating';

-- name: ValidateSessionSandboxGeneration :one
SELECT session_id FROM agent_session_sandbox
WHERE session_id = sqlc.arg(session_id)
  AND generation = sqlc.arg(generation)
  AND executor_boot_id = sqlc.arg(executor_boot_id)
  AND run_id = sqlc.arg(run_id)
  AND state = 'active';

-- name: FenceSessionSandboxGeneration :execrows
UPDATE agent_session_sandbox
SET state = 'fenced', fenced_at = now(), updated_at = now()
WHERE session_id = sqlc.arg(session_id)
  AND generation = sqlc.arg(generation)
  AND executor_boot_id = sqlc.arg(executor_boot_id)
  AND run_id = sqlc.arg(run_id)
  AND state IN ('creating', 'active');

-- name: DestroySessionSandboxGeneration :execrows
UPDATE agent_session_sandbox
SET state = 'destroyed', destroyed_at = now(), updated_at = now()
WHERE session_id = sqlc.arg(session_id)
  AND generation = sqlc.arg(generation)
  AND state = 'fenced';

-- name: FenceRecoverableSessionSandbox :execrows
UPDATE agent_session_sandbox AS sandbox
SET state = 'fenced', fenced_at = now(), updated_at = now()
WHERE sandbox.state IN ('creating', 'active')
  AND NOT EXISTS (
      SELECT 1 FROM runtime_executor_boot AS boot
      WHERE boot.id = sandbox.executor_boot_id
        AND boot.status = 'running'
        AND boot.heartbeat_at > now() - make_interval(secs => sqlc.arg(stale_seconds)::integer)
  )
  AND NOT EXISTS (
      SELECT 1 FROM agent_run AS run
      WHERE run.id = sandbox.run_id
        AND run.session_id = sandbox.session_id
        AND run.executor_boot_id = sandbox.executor_boot_id
        AND run.status = 'running'
        AND run.abort_requested_at IS NULL
        AND run.lease_expires_at > now()
  );

-- name: ListRecoverableFencedSessionSandbox :many
SELECT sandbox.* FROM agent_session_sandbox AS sandbox
WHERE sandbox.state = 'fenced'
  AND NOT EXISTS (
      SELECT 1 FROM runtime_executor_boot AS boot
      WHERE boot.id = sandbox.executor_boot_id
        AND boot.status = 'running'
        AND boot.heartbeat_at > now() - make_interval(secs => sqlc.arg(stale_seconds)::integer)
  )
ORDER BY sandbox.updated_at, sandbox.session_id;

-- name: CreateSessionSandboxProcess :execrows
INSERT INTO agent_session_sandbox_process (session_id, generation, pid, start_time)
SELECT sqlc.arg(session_id), sqlc.arg(generation), sqlc.arg(pid), sqlc.arg(start_time)
WHERE EXISTS (
    SELECT 1 FROM agent_session_sandbox
    WHERE session_id = sqlc.arg(session_id)
      AND generation = sqlc.arg(generation)
      AND executor_boot_id = sqlc.arg(executor_boot_id)
      AND run_id = sqlc.arg(run_id)
      AND state = 'active'
)
ON CONFLICT DO NOTHING;

-- name: ListSessionSandboxProcess :many
SELECT process.* FROM agent_session_sandbox_process AS process
WHERE process.session_id = sqlc.arg(session_id)
  AND process.generation = sqlc.arg(generation)
ORDER BY process.created_at, process.pid;
