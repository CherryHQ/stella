-- name: CreateGoal :one
INSERT INTO agent_goal (
    id, user_id, agent_id, project_id, parent_id, root_id, depth, position,
    title, intent, kind, priority, required,
    acceptance_contract, convergence_policy, review_policy,
    lifecycle, context, dispatch_hint, workflow_id, workflow_version, idempotency_key
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22)
RETURNING *;

-- name: CreateGoalIfAbsent :one
INSERT INTO agent_goal (
    id, user_id, agent_id, project_id, parent_id, root_id, depth, position,
    title, intent, kind, priority, required,
    acceptance_contract, convergence_policy, review_policy,
    lifecycle, context, dispatch_hint, workflow_id, workflow_version, idempotency_key
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22)
ON CONFLICT (id) DO UPDATE SET id = agent_goal.id
RETURNING *;

-- name: GetGoal :one
SELECT * FROM agent_goal WHERE id = $1;

-- name: GetGoalByIdempotencyKey :one
SELECT * FROM agent_goal
WHERE user_id = $1 AND idempotency_key = $2;

-- Root goals (goals: parent_id IS NULL) for a user, scoped to an agent
-- and narrowed by lifecycle / terminal-ness / project / workflow / free-text.
-- Every narg is optional: NULL matches all. terminal: false = active
-- (non-terminal) only, true = history (done) only, NULL = both.
-- name: ListRootGoal :many
SELECT * FROM agent_goal
WHERE parent_id IS NULL
  AND user_id = sqlc.arg(user_id)
  AND (sqlc.narg(agent_id)::text IS NULL OR agent_id = sqlc.narg(agent_id)::text)
  AND (sqlc.narg(project_id)::uuid IS NULL OR project_id = sqlc.narg(project_id)::uuid)
  AND (sqlc.narg(workflow_id)::uuid IS NULL OR workflow_id = sqlc.narg(workflow_id)::uuid)
  AND (sqlc.narg(lifecycle)::text IS NULL OR lifecycle = sqlc.narg(lifecycle)::text)
  AND (sqlc.narg(terminal)::boolean IS NULL
       OR (lifecycle = 'done') = sqlc.narg(terminal)::boolean)
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
  AND (sqlc.narg(workflow_id)::uuid IS NULL OR workflow_id = sqlc.narg(workflow_id)::uuid)
  AND (sqlc.narg(lifecycle)::text IS NULL OR lifecycle = sqlc.narg(lifecycle)::text)
  AND (sqlc.narg(terminal)::boolean IS NULL
       OR (lifecycle = 'done') = sqlc.narg(terminal)::boolean)
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
    done_reason = sqlc.arg(done_reason),
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
  AND lifecycle = 'pending'
  AND active_attempt_id IS NULL;

-- name: ClearGoalActiveAttempt :exec
UPDATE agent_goal SET
    active_attempt_id = NULL,
    updated_at = now()
WHERE id = $1;

-- name: AcceptGoal :execrows
UPDATE agent_goal SET
    lifecycle = 'done',
    done_reason = 'accepted',
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
WHERE id = sqlc.arg(id) AND lifecycle IN ('pending', 'active');

-- name: IncrementGoalFlakyCount :one
UPDATE agent_goal SET
    flaky_count = flaky_count + 1,
    updated_at = now()
WHERE id = $1
RETURNING flaky_count;

-- name: IncrementGoalBudgetBonus :exec
UPDATE agent_goal SET
    budget_bonus = budget_bonus + 1,
    updated_at = now()
WHERE id = $1;

-- name: SetGoalPlan :exec
-- Write a composite's decomposition plan (children + edges). Set by a
-- decomposition attempt before materialize; for review_policy='human' the plan
-- sits here while the goal is blocked(needs_plan_approval) until a human acts.
UPDATE agent_goal SET
    plan = sqlc.arg(plan),
    updated_at = now()
WHERE id = sqlc.arg(id);

-- name: MarkGoalPlanned :execrows
-- The materialize fence (CAS): stamp planned_at exactly once. A second concurrent
-- materialize gets 0 rows and must treat it as an idempotent no-op, so children
-- are never double-created. Replaces the old materialized revision unique index.
UPDATE agent_goal SET
    planned_at = now(),
    updated_at = now()
WHERE id = sqlc.arg(id) AND planned_at IS NULL;

-- name: StampGoalWorkflow :exec
-- Mark composite children whose sub-plan is frozen by a workflow. The
-- decomposition dispatcher filters workflow_id IS NULL, so stamped children are
-- never picked up for autonomous replanning between walk transactions. Runs in
-- the same tx as the parent layer materialize so the exclusion is atomic.
UPDATE agent_goal SET
    workflow_id = sqlc.arg(workflow_id)::uuid,
    workflow_version = sqlc.arg(workflow_version)::integer,
    updated_at = now()
WHERE id = ANY(sqlc.arg(ids)::text[]);

-- name: ListDispatchableLeaves :many
SELECT * FROM agent_goal
WHERE lifecycle = 'pending'
  AND active_attempt_id IS NULL
  AND kind = 'leaf'
ORDER BY priority DESC, created_at ASC
LIMIT $1;

-- name: ListDecomposableComposites :many
-- Composites awaiting autonomous decomposition: freshly created (draft), not
-- planned yet, and not workflow roots. A workflow root carries workflow_id and
-- is materialized only by workflow replay; nested workflow composites do not
-- carry workflow_id and remain planner-eligible when their sub-plan is not frozen.
SELECT * FROM agent_goal
WHERE kind = 'composite'
  AND lifecycle = 'draft'
  AND planned_at IS NULL
  AND workflow_id IS NULL
ORDER BY priority DESC, created_at ASC
LIMIT $1;

-- name: ListRollupCandidates :many
SELECT * FROM agent_goal
WHERE kind = 'composite'
  AND lifecycle = 'active'
  AND planned_at IS NOT NULL
ORDER BY updated_at ASC
LIMIT $1;

-- name: GetRequiredChildRollupCounts :one
SELECT
    COUNT(*)::bigint AS total,
    COUNT(*) FILTER (WHERE c.lifecycle = 'done' AND c.done_reason = 'accepted')::bigint AS accepted,
    COUNT(*) FILTER (WHERE c.lifecycle = 'done' AND c.done_reason IN ('failed', 'cancelled'))::bigint AS failed,
    COUNT(*) FILTER (
        WHERE c.lifecycle = 'blocked'
           OR EXISTS (
                SELECT 1 FROM agent_goal_edge e
                JOIN agent_goal u ON u.id = e.upstream_id
                WHERE e.goal_id = c.id
                  AND e.edge_kind = 'hard'
                  AND e.waived_at IS NULL
                  AND u.lifecycle = 'done'
                  AND u.done_reason IN ('failed', 'cancelled')
                  AND e.on_failure = 'block'
           )
    )::bigint AS blocked,
    COUNT(*) FILTER (
        WHERE EXISTS (
                SELECT 1 FROM agent_goal_edge e
                JOIN agent_goal u ON u.id = e.upstream_id
                WHERE e.goal_id = c.id
                  AND e.edge_kind = 'hard'
                  AND e.waived_at IS NULL
                  AND u.lifecycle = 'done'
                  AND u.done_reason IN ('failed', 'cancelled')
                  AND e.on_failure = 'fail'
        )
    )::bigint AS dep_failed
FROM agent_goal c
WHERE c.parent_id = sqlc.arg(parent_id)::text AND c.required;

-- name: ListZombieGoals :many
-- Liveness backstop: non-terminal goals parked in a state nothing drives.
-- Two classes:
--   1. A pending composite: the dispatcher claims pending LEAVES and decomposes
--      DRAFT composites, so nothing ever picks a composite out of pending. The
--      transition table rejects writing this state; the scan catches legacy
--      rows and out-of-band writes.
--   2. An active goal with no active attempt pointer and no queued/running
--      attempt, where activity cannot come from anywhere else: a leaf is only
--      active while claimed, and an UNPLANNED active composite is only active
--      while its decomposition attempt runs. A planned active composite is
--      excluded -- the rollup drives it off its children.
-- The updated_at grace keeps a row mid-transition in another tx from reading
-- as a zombie.
SELECT * FROM agent_goal
WHERE lifecycle != 'done'
  AND updated_at < now() - interval '5 minutes'
  AND (
    (kind = 'composite' AND lifecycle = 'pending')
    OR (
      lifecycle = 'active'
      AND active_attempt_id IS NULL
      AND (kind = 'leaf' OR planned_at IS NULL)
      AND NOT EXISTS (
        SELECT 1 FROM agent_goal_attempt a
        WHERE a.goal_id = agent_goal.id AND a.status IN ('queued', 'running')
      )
    )
  )
ORDER BY updated_at ASC
LIMIT $1;

-- name: ListGoalsBlockedNeedsVerdict :many
-- Goals parked blocked(needs_verdict): a required judgment item has no valid
-- verdict. The dispatcher (scanAndReview) drives an agent reviewer for any with a
-- pending authority=agent item (contract section 10.13); the rest await a human.
-- Both leaf and composite goals can carry an authored judgment contract.
SELECT * FROM agent_goal
WHERE lifecycle = 'blocked'
  AND block_reason = 'needs_verdict'
ORDER BY priority DESC, created_at ASC
LIMIT $1;

-- name: CancelGoal :exec
UPDATE agent_goal SET
    lifecycle = 'done',
    done_reason = 'cancelled',
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

-- name: ListGoalsByWorkflow :many
SELECT * FROM agent_goal
WHERE workflow_id = $1
  AND parent_id IS NULL
ORDER BY created_at DESC, id DESC
LIMIT $2;

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
JOIN agent_goal r ON r.id = d.root_id
WHERE d.user_id = sqlc.arg(user_id)
  AND d.archived_at IS NULL
  AND (sqlc.narg(agent_id)::text IS NULL OR d.agent_id = sqlc.narg(agent_id)::text)
  AND (
        (
          d.lifecycle = 'blocked'
                    AND r.lifecycle != 'done'
        )
        OR (d.lifecycle = 'done' AND d.done_reason = 'failed' AND d.updated_at >= sqlc.arg(since) AND d.parent_id IS NULL)
      )
ORDER BY d.updated_at DESC, d.id DESC
LIMIT sqlc.arg(limit_count);

-- Transaction-scoped advisory lock serializing read-modify-write sequence
-- allocation for one goal. The acceptance ledger seq and attempt_no
-- are each GetMax->+1->insert under Read Committed, which PostgreSQL runs in
-- parallel across writers (and nodes): without this, two writers read the same
-- max and compute the same next value, silently duplicating the acceptance seq
-- (no unique backstop) or colliding on the attempt_no unique index.
-- Held until the enclosing tx ends. The 'goal:' prefix keeps unrelated entities
-- out of this goal's slot in the shared 64-bit lock space (matching
-- AdvisoryXactLock / LockSchedJobForRun).
-- name: LockGoalForWrite :exec
SELECT pg_advisory_xact_lock(hashtextextended('goal:' || sqlc.arg(goal_id)::text, 0));
