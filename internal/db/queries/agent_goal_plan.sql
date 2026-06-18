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
