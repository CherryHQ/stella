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
