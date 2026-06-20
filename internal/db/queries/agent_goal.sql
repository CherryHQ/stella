-- name: CreateGoal :one
INSERT INTO agent_goal (
    id, user_id, agent_id, project_id, parent_id, root_id, depth, position,
    session_id, title, intent, kind, priority, required,
    acceptance_contract, convergence_policy, review_policy,
    lifecycle, context, dispatch_hint
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20)
RETURNING *;

-- name: GetGoal :one
SELECT * FROM agent_goal WHERE id = $1;

-- Root goals (goals: parent_id IS NULL) for a user, scoped to an agent
-- and narrowed by lifecycle / terminal-ness / project / free-text. Every narg is
-- optional: NULL matches all. terminal: false = active (non-terminal) only, true =
-- history (terminal) only, NULL = both. The terminal set is the four end states.
-- name: ListRootGoal :many
SELECT * FROM agent_goal
WHERE parent_id IS NULL
  AND user_id = sqlc.arg(user_id)
  AND (sqlc.narg(agent_id)::text IS NULL OR agent_id = sqlc.narg(agent_id)::text)
  AND (sqlc.narg(project_id)::uuid IS NULL OR project_id = sqlc.narg(project_id)::uuid)
  AND (sqlc.narg(lifecycle)::text IS NULL OR lifecycle = sqlc.narg(lifecycle)::text)
  AND (sqlc.narg(terminal)::boolean IS NULL
       OR (lifecycle IN ('accepted', 'rejected_final', 'abandoned', 'cancelled')) = sqlc.narg(terminal)::boolean)
  AND (sqlc.narg(q)::text IS NULL OR title ILIKE '%' || sqlc.narg(q) || '%' OR intent ILIKE '%' || sqlc.narg(q) || '%')
  AND (sqlc.arg(include_archived)::boolean OR archived_at IS NULL)
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg('limit') OFFSET sqlc.arg('offset');

-- CountRootGoal mirrors ListRootGoal's filter so a list's reported
-- total is exact, and the active/history/archived header badges are three cheap
-- counts that vary only their terminal/include_archived args.
-- name: CountRootGoal :one
SELECT CAST(COUNT(*) AS BIGINT) FROM agent_goal
WHERE parent_id IS NULL
  AND user_id = sqlc.arg(user_id)
  AND (sqlc.narg(agent_id)::text IS NULL OR agent_id = sqlc.narg(agent_id)::text)
  AND (sqlc.narg(project_id)::uuid IS NULL OR project_id = sqlc.narg(project_id)::uuid)
  AND (sqlc.narg(lifecycle)::text IS NULL OR lifecycle = sqlc.narg(lifecycle)::text)
  AND (sqlc.narg(terminal)::boolean IS NULL
       OR (lifecycle IN ('accepted', 'rejected_final', 'abandoned', 'cancelled')) = sqlc.narg(terminal)::boolean)
  AND (sqlc.narg(q)::text IS NULL OR title ILIKE '%' || sqlc.narg(q) || '%' OR intent ILIKE '%' || sqlc.narg(q) || '%')
  AND (sqlc.arg(include_archived)::boolean OR archived_at IS NULL);

-- name: ListGoalChildren :many
SELECT * FROM agent_goal
WHERE parent_id = $1
ORDER BY position ASC, id ASC;

-- name: ListGoalByRoot :many
SELECT * FROM agent_goal
WHERE root_id = $1
ORDER BY depth ASC, position ASC, id ASC;

-- name: ListGoalSubtree :many
WITH RECURSIVE subtree(id) AS (
    SELECT d0.id FROM agent_goal d0 WHERE d0.id = sqlc.arg(id)
    UNION ALL
    SELECT d.id FROM agent_goal d
    JOIN subtree s ON d.parent_id = s.id
)
SELECT d.* FROM agent_goal d
JOIN subtree s ON d.id = s.id
ORDER BY d.depth ASC, d.position ASC, d.id ASC;

-- name: UpdateGoalIntent :exec
UPDATE agent_goal SET
    title = $1,
    intent = $2,
    acceptance_contract = $3,
    convergence_policy = $4,
    review_policy = $5,
    priority = $6,
    updated_at = now()
WHERE id = $7;

-- name: TransitionGoalLifecycle :execrows
UPDATE agent_goal SET
    lifecycle = sqlc.arg(to_lifecycle),
    block_reason = sqlc.arg(block_reason),
    updated_at = now()
WHERE id = sqlc.arg(id) AND lifecycle = sqlc.arg(from_lifecycle);

-- name: ClaimGoal :execrows
UPDATE agent_goal SET
    lifecycle = 'active',
    active_attempt_id = sqlc.arg(active_attempt_id),
    attempt_count = attempt_count + 1,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND lifecycle = 'ready'
  AND active_attempt_id IS NULL;

-- name: ClearGoalActiveAttempt :exec
UPDATE agent_goal SET
    active_attempt_id = NULL,
    updated_at = now()
WHERE id = $1;

-- name: AcceptGoal :execrows
UPDATE agent_goal SET
    lifecycle = 'accepted',
    acceptance_state = 'passed',
    accepted_output = sqlc.arg(accepted_output),
    accepted_at = now(),
    active_attempt_id = NULL,
    updated_at = now()
WHERE id = sqlc.arg(id) AND lifecycle = 'active';

-- name: SetGoalAcceptanceState :execrows
UPDATE agent_goal SET
    acceptance_state = sqlc.arg(acceptance_state),
    acceptance_seq = sqlc.arg(acceptance_seq),
    updated_at = now()
WHERE id = sqlc.arg(id) AND acceptance_seq < sqlc.arg(acceptance_seq);

-- name: BlockGoal :execrows
UPDATE agent_goal SET
    lifecycle = 'blocked',
    block_reason = sqlc.arg(block_reason),
    active_attempt_id = NULL,
    updated_at = now()
WHERE id = sqlc.arg(id) AND lifecycle IN ('ready', 'active');

-- name: IncrGoalRequiredAccepted :exec
UPDATE agent_goal SET
    required_accepted = required_accepted + 1,
    updated_at = now()
WHERE id = $1;

-- name: IncrGoalRequiredFailed :exec
UPDATE agent_goal SET
    required_failed = required_failed + 1,
    updated_at = now()
WHERE id = $1;

-- name: IncrGoalRequiredBlocked :exec
UPDATE agent_goal SET
    required_blocked = required_blocked + 1,
    updated_at = now()
WHERE id = $1;

-- name: DecrGoalRequiredBlocked :exec
UPDATE agent_goal SET
    required_blocked = required_blocked - 1,
    updated_at = now()
WHERE id = $1 AND required_blocked > 0;

-- name: SetGoalRequiredTotal :exec
UPDATE agent_goal SET
    required_total = sqlc.arg(required_total),
    updated_at = now()
WHERE id = sqlc.arg(id);

-- name: SetGoalAcceptedRevision :exec
UPDATE agent_goal SET
    accepted_revision_id = sqlc.arg(accepted_revision_id),
    updated_at = now()
WHERE id = sqlc.arg(id);

-- name: ReconcileGoalCounters :exec
UPDATE agent_goal SET
    required_total = (
        SELECT CAST(COUNT(*) AS BIGINT) FROM agent_goal c
        WHERE c.parent_id = agent_goal.id AND c.required
    ),
    required_accepted = (
        SELECT CAST(COUNT(*) AS BIGINT) FROM agent_goal c
        WHERE c.parent_id = agent_goal.id AND c.required
          AND c.lifecycle = 'accepted'
    ),
    required_failed = (
        SELECT CAST(COUNT(*) AS BIGINT) FROM agent_goal c
        WHERE c.parent_id = agent_goal.id AND c.required
          AND c.lifecycle IN ('rejected_final', 'abandoned', 'cancelled')
    ),
    required_blocked = (
        SELECT CAST(COUNT(*) AS BIGINT) FROM agent_goal c
        WHERE c.parent_id = agent_goal.id AND c.required
          AND c.lifecycle = 'blocked'
    ),
    updated_at = now()
WHERE agent_goal.id = $1;

-- name: ListDispatchableLeaves :many
SELECT * FROM agent_goal
WHERE lifecycle = 'ready'
  AND active_attempt_id IS NULL
  AND kind = 'leaf'
ORDER BY priority DESC, created_at ASC
LIMIT $1;

-- name: ListRollupCandidates :many
SELECT * FROM agent_goal
WHERE kind = 'composite'
  AND lifecycle = 'active'
  AND required_total > 0
  AND required_accepted >= required_total
ORDER BY updated_at ASC
LIMIT $1;

-- name: ListStalledComposites :many
SELECT * FROM agent_goal
WHERE kind = 'composite'
  AND lifecycle = 'active'
  AND required_accepted < required_total
ORDER BY updated_at ASC
LIMIT $1;

-- name: CancelGoal :exec
UPDATE agent_goal SET
    lifecycle = 'cancelled',
    cancelled_at = now(),
    active_attempt_id = NULL,
    updated_at = now()
WHERE id = $1;

-- name: ArchiveGoal :exec
UPDATE agent_goal SET
    archived_at = now(),
    updated_at = now()
WHERE id = $1;

-- name: UnarchiveGoal :exec
UPDATE agent_goal SET
    archived_at = NULL,
    updated_at = now()
WHERE id = $1;

-- name: ConsumeDispatchHint :exec
UPDATE agent_goal SET
    dispatch_hint = sqlc.arg(dispatch_hint),
    updated_at = now()
WHERE id = sqlc.arg(id);

-- Goals needing user attention, for the inbox. Open blocks surface at any
-- age (they wait on the user); terminal failures are windowed like failed runs.
-- The handler splits rows into inbox kinds by lifecycle/block_reason.
-- name: ListInboxGoals :many
SELECT
    d.id,
    d.agent_id,
    d.project_id,
    d.title,
    d.intent,
    d.lifecycle,
    d.block_reason,
    d.updated_at,
    d.created_at
FROM agent_goal d
WHERE d.user_id = sqlc.arg(user_id)
  AND d.archived_at IS NULL
  AND (sqlc.narg(agent_id)::text IS NULL OR d.agent_id = sqlc.narg(agent_id)::text)
  AND (
        d.lifecycle = 'blocked'
        OR (d.lifecycle IN ('rejected_final', 'abandoned') AND d.updated_at >= sqlc.arg(since))
      )
ORDER BY d.updated_at DESC, d.id DESC
LIMIT sqlc.arg(limit_count);
