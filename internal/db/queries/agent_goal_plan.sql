-- Issue #525: the accepted-plan gate. One plan row per goal (goal_id UNIQUE).
-- Mutation queries (upsert-pending, accept, approve, promote-in-materialize-tx)
-- land in their consuming phase alongside tested callers; this file ships the
-- foundational create/read trio every later phase builds on.

-- name: CreateAgentGoalPlan :one
INSERT INTO agent_goal_plan (id, goal_id, status, review_policy, pending_content_json, source_run_id)
VALUES (?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetAgentGoalPlan :one
SELECT * FROM agent_goal_plan WHERE id = ?;

-- name: GetAgentGoalPlanByGoal :one
SELECT * FROM agent_goal_plan WHERE goal_id = ?;

-- UpsertAgentGoalPlanPending writes an in-flight edit to the goal's single plan
-- row (goal_id UNIQUE): insert when absent, else replace the pending draft and
-- status. content_json (last materialized content) is never touched here; only
-- MaterializeGoalPlan promotes pending -> content. #525 (codex BLOCKER 4).
-- :exec (not RETURNING) because sqlc's SQLite dialect rejects ON CONFLICT ...
-- DO UPDATE ... RETURNING; callers re-Get by goal_id after the upsert.
-- name: UpsertAgentGoalPlanPending :exec
INSERT INTO agent_goal_plan (id, goal_id, status, review_policy, pending_content_json, source_run_id)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(goal_id) DO UPDATE SET
    status = excluded.status,
    review_policy = excluded.review_policy,
    pending_content_json = excluded.pending_content_json,
    source_run_id = excluded.source_run_id,
    updated_at = datetime('now');

-- name: SetAgentGoalPlanAccepted :exec
UPDATE agent_goal_plan
SET status = ?, accepted_at = ?, updated_at = datetime('now')
WHERE id = ?;

-- SetAgentGoalPlanInReview moves a draft plan with a pending edit into review.
-- Guarded WHERE status='draft' so a second submit on an already in_review plan is
-- a no-op (0 rows), which the caller maps to a refusal. #525.
-- name: SetAgentGoalPlanInReview :execrows
UPDATE agent_goal_plan
SET status = ?, updated_at = datetime('now')
WHERE id = ? AND status = 'draft';

-- SetAgentGoalPlanApproved closes a plan review: status approved + the deciding
-- review + accepted_at, so the next MaterializeGoalPlan can promote it. content_json
-- is untouched; promotion happens only in the materialize tx (2nd-pass B1). #525.
-- name: SetAgentGoalPlanApproved :exec
UPDATE agent_goal_plan
SET status = ?, approved_review_id = ?, accepted_at = ?, updated_at = datetime('now')
WHERE id = ?;

-- SetAgentGoalPlanStatus is the back-to-draft path for a rejected / changes-requested
-- plan review. content_json is never touched here, so a rejected replan leaves the
-- running goal's materialized work exactly as it was. #525.
-- name: SetAgentGoalPlanStatus :exec
UPDATE agent_goal_plan
SET status = ?, updated_at = datetime('now')
WHERE id = ?;

-- ClearAgentGoalPlanPending discards an in-flight edit (a rejected replan that
-- should not be kept). content_json stays as the last materialized content. #525.
-- name: ClearAgentGoalPlanPending :exec
UPDATE agent_goal_plan
SET pending_content_json = NULL, updated_at = datetime('now')
WHERE id = ?;

-- PromoteAgentGoalPlanMaterialized promotes the in-flight edit to the
-- materialized content and stamps materialized_at, atomically inside the
-- materialize tx (D1/2nd-pass B1). COALESCE keeps content_json when there is no
-- pending edit (a re-materialize with no staged change), so the NOT NULL column
-- is never nulled. #525.
-- name: PromoteAgentGoalPlanMaterialized :exec
UPDATE agent_goal_plan
SET content_json = COALESCE(pending_content_json, content_json),
    pending_content_json = NULL,
    materialized_at = ?,
    updated_at = datetime('now')
WHERE id = ?;
