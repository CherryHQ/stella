-- name: CreateDeliverable :one
INSERT INTO agent_dlv_deliverable (
    id, user_id, agent_id, project_id, parent_id, root_id, depth, position,
    session_id, title, intent, kind, priority, required,
    acceptance_contract, convergence_policy, review_policy,
    lifecycle, context, dispatch_hint
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetDeliverable :one
SELECT * FROM agent_dlv_deliverable WHERE id = ?;

-- Root deliverables (goals: parent_id IS NULL) for a user, scoped to an agent
-- and narrowed by lifecycle / terminal-ness / project / free-text. Every narg is
-- optional: NULL matches all. terminal: 0 = active (non-terminal) only, 1 =
-- history (terminal) only, NULL = both. The terminal set is the four end states.
-- name: ListRootDeliverable :many
SELECT * FROM agent_dlv_deliverable
WHERE parent_id IS NULL
  AND user_id = sqlc.arg(user_id)
  AND (sqlc.narg(agent_id) IS NULL OR agent_id = sqlc.narg(agent_id))
  AND (sqlc.narg(project_id) IS NULL OR project_id = sqlc.narg(project_id))
  AND (sqlc.narg(lifecycle) IS NULL OR lifecycle = sqlc.narg(lifecycle))
  AND (sqlc.narg(terminal) IS NULL
       OR (sqlc.narg(terminal) != 0 AND lifecycle IN ('accepted', 'rejected_final', 'abandoned', 'cancelled'))
       OR (sqlc.narg(terminal) = 0 AND lifecycle NOT IN ('accepted', 'rejected_final', 'abandoned', 'cancelled')))
  AND (sqlc.narg(q) IS NULL OR title LIKE '%' || sqlc.narg(q) || '%' OR intent LIKE '%' || sqlc.narg(q) || '%')
  AND (sqlc.arg(include_archived) != 0 OR archived_at IS NULL)
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(limit) OFFSET sqlc.arg(offset);

-- CountRootDeliverable mirrors ListRootDeliverable's filter so a list's reported
-- total is exact, and the active/history/archived header badges are three cheap
-- counts that vary only their terminal/include_archived args.
-- name: CountRootDeliverable :one
SELECT CAST(COUNT(*) AS INTEGER) FROM agent_dlv_deliverable
WHERE parent_id IS NULL
  AND user_id = sqlc.arg(user_id)
  AND (sqlc.narg(agent_id) IS NULL OR agent_id = sqlc.narg(agent_id))
  AND (sqlc.narg(project_id) IS NULL OR project_id = sqlc.narg(project_id))
  AND (sqlc.narg(lifecycle) IS NULL OR lifecycle = sqlc.narg(lifecycle))
  AND (sqlc.narg(terminal) IS NULL
       OR (sqlc.narg(terminal) != 0 AND lifecycle IN ('accepted', 'rejected_final', 'abandoned', 'cancelled'))
       OR (sqlc.narg(terminal) = 0 AND lifecycle NOT IN ('accepted', 'rejected_final', 'abandoned', 'cancelled')))
  AND (sqlc.narg(q) IS NULL OR title LIKE '%' || sqlc.narg(q) || '%' OR intent LIKE '%' || sqlc.narg(q) || '%')
  AND (sqlc.arg(include_archived) != 0 OR archived_at IS NULL);

-- name: ListDeliverableChildren :many
SELECT * FROM agent_dlv_deliverable
WHERE parent_id = ?
ORDER BY position ASC, id ASC;

-- name: ListDeliverableByRoot :many
SELECT * FROM agent_dlv_deliverable
WHERE root_id = ?
ORDER BY depth ASC, position ASC, id ASC;

-- name: ListDeliverableSubtree :many
WITH RECURSIVE subtree(id) AS (
    SELECT d0.id FROM agent_dlv_deliverable d0 WHERE d0.id = sqlc.arg(id)
    UNION ALL
    SELECT d.id FROM agent_dlv_deliverable d
    JOIN subtree s ON d.parent_id = s.id
)
SELECT d.* FROM agent_dlv_deliverable d
JOIN subtree s ON d.id = s.id
ORDER BY d.depth ASC, d.position ASC, d.id ASC;

-- name: UpdateDeliverableIntent :exec
UPDATE agent_dlv_deliverable SET
    title = ?,
    intent = ?,
    acceptance_contract = ?,
    convergence_policy = ?,
    review_policy = ?,
    priority = ?,
    updated_at = datetime('now')
WHERE id = ?;

-- name: TransitionDeliverableLifecycle :execrows
UPDATE agent_dlv_deliverable SET
    lifecycle = sqlc.arg(to_lifecycle),
    block_reason = sqlc.arg(block_reason),
    updated_at = datetime('now')
WHERE id = sqlc.arg(id) AND lifecycle = sqlc.arg(from_lifecycle);

-- name: ClaimDeliverable :execrows
UPDATE agent_dlv_deliverable SET
    lifecycle = 'active',
    active_attempt_id = sqlc.arg(active_attempt_id),
    attempt_count = attempt_count + 1,
    updated_at = datetime('now')
WHERE id = sqlc.arg(id)
  AND lifecycle = 'ready'
  AND active_attempt_id IS NULL;

-- name: ClearDeliverableActiveAttempt :exec
UPDATE agent_dlv_deliverable SET
    active_attempt_id = NULL,
    updated_at = datetime('now')
WHERE id = ?;

-- name: AcceptDeliverable :execrows
UPDATE agent_dlv_deliverable SET
    lifecycle = 'accepted',
    acceptance_state = 'passed',
    accepted_output = sqlc.arg(accepted_output),
    accepted_at = datetime('now'),
    active_attempt_id = NULL,
    updated_at = datetime('now')
WHERE id = sqlc.arg(id) AND lifecycle = 'active';

-- name: SetDeliverableAcceptanceState :execrows
UPDATE agent_dlv_deliverable SET
    acceptance_state = sqlc.arg(acceptance_state),
    acceptance_seq = sqlc.arg(acceptance_seq),
    updated_at = datetime('now')
WHERE id = sqlc.arg(id) AND acceptance_seq < sqlc.arg(acceptance_seq);

-- name: BlockDeliverable :execrows
UPDATE agent_dlv_deliverable SET
    lifecycle = 'blocked',
    block_reason = sqlc.arg(block_reason),
    active_attempt_id = NULL,
    updated_at = datetime('now')
WHERE id = sqlc.arg(id) AND lifecycle IN ('ready', 'active');

-- name: IncrDeliverableRequiredAccepted :exec
UPDATE agent_dlv_deliverable SET
    required_accepted = required_accepted + 1,
    updated_at = datetime('now')
WHERE id = ?;

-- name: IncrDeliverableRequiredFailed :exec
UPDATE agent_dlv_deliverable SET
    required_failed = required_failed + 1,
    updated_at = datetime('now')
WHERE id = ?;

-- name: IncrDeliverableRequiredBlocked :exec
UPDATE agent_dlv_deliverable SET
    required_blocked = required_blocked + 1,
    updated_at = datetime('now')
WHERE id = ?;

-- name: DecrDeliverableRequiredBlocked :exec
UPDATE agent_dlv_deliverable SET
    required_blocked = required_blocked - 1,
    updated_at = datetime('now')
WHERE id = ? AND required_blocked > 0;

-- name: SetDeliverableRequiredTotal :exec
UPDATE agent_dlv_deliverable SET
    required_total = sqlc.arg(required_total),
    updated_at = datetime('now')
WHERE id = sqlc.arg(id);

-- name: SetDeliverableAcceptedRevision :exec
UPDATE agent_dlv_deliverable SET
    accepted_revision_id = sqlc.arg(accepted_revision_id),
    updated_at = datetime('now')
WHERE id = sqlc.arg(id);

-- name: ReconcileDeliverableCounters :exec
UPDATE agent_dlv_deliverable SET
    required_total = (
        SELECT CAST(COUNT(*) AS INTEGER) FROM agent_dlv_deliverable c
        WHERE c.parent_id = agent_dlv_deliverable.id AND c.required = 1
    ),
    required_accepted = (
        SELECT CAST(COUNT(*) AS INTEGER) FROM agent_dlv_deliverable c
        WHERE c.parent_id = agent_dlv_deliverable.id AND c.required = 1
          AND c.lifecycle = 'accepted'
    ),
    required_failed = (
        SELECT CAST(COUNT(*) AS INTEGER) FROM agent_dlv_deliverable c
        WHERE c.parent_id = agent_dlv_deliverable.id AND c.required = 1
          AND c.lifecycle IN ('rejected_final', 'abandoned', 'cancelled')
    ),
    required_blocked = (
        SELECT CAST(COUNT(*) AS INTEGER) FROM agent_dlv_deliverable c
        WHERE c.parent_id = agent_dlv_deliverable.id AND c.required = 1
          AND c.lifecycle = 'blocked'
    ),
    updated_at = datetime('now')
WHERE agent_dlv_deliverable.id = ?;

-- name: ListDispatchableLeaves :many
SELECT * FROM agent_dlv_deliverable
WHERE lifecycle = 'ready'
  AND active_attempt_id IS NULL
  AND kind = 'leaf'
ORDER BY priority DESC, created_at ASC
LIMIT ?;

-- name: ListRollupCandidates :many
SELECT * FROM agent_dlv_deliverable
WHERE kind = 'composite'
  AND lifecycle = 'active'
  AND required_total > 0
  AND required_accepted >= required_total
ORDER BY updated_at ASC
LIMIT ?;

-- name: ListStalledComposites :many
SELECT * FROM agent_dlv_deliverable
WHERE kind = 'composite'
  AND lifecycle = 'active'
  AND required_accepted < required_total
ORDER BY updated_at ASC
LIMIT ?;

-- name: CancelDeliverable :exec
UPDATE agent_dlv_deliverable SET
    lifecycle = 'cancelled',
    cancelled_at = datetime('now'),
    active_attempt_id = NULL,
    updated_at = datetime('now')
WHERE id = ?;

-- name: ArchiveDeliverable :exec
UPDATE agent_dlv_deliverable SET
    archived_at = datetime('now'),
    updated_at = datetime('now')
WHERE id = ?;

-- name: UnarchiveDeliverable :exec
UPDATE agent_dlv_deliverable SET
    archived_at = NULL,
    updated_at = datetime('now')
WHERE id = ?;

-- name: ConsumeDispatchHint :exec
UPDATE agent_dlv_deliverable SET
    dispatch_hint = sqlc.arg(dispatch_hint),
    updated_at = datetime('now')
WHERE id = sqlc.arg(id);

-- Deliverables needing user attention, for the inbox. Open blocks surface at any
-- age (they wait on the user); terminal failures are windowed like failed runs.
-- The handler splits rows into inbox kinds by lifecycle/block_reason.
-- name: ListInboxDeliverables :many
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
FROM agent_dlv_deliverable d
WHERE d.user_id = sqlc.arg(user_id)
  AND d.archived_at IS NULL
  AND (sqlc.narg(agent_id) IS NULL OR d.agent_id = sqlc.narg(agent_id))
  AND (
        d.lifecycle = 'blocked'
        OR (d.lifecycle IN ('rejected_final', 'abandoned') AND d.updated_at >= sqlc.arg(since))
      )
ORDER BY d.updated_at DESC, d.id DESC
LIMIT sqlc.arg(limit_count);
