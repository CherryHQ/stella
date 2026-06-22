-- +goose Up
-- Collapse the agent_goal_revision entity into the composite goal itself.
-- A composite's decomposition plan was a first-class row with a 5-state FSM
-- (draft/in_review/accepted/rejected/superseded), version numbers, and its own
-- table. Replan is not supported (a composite is decomposed exactly once), so
-- the versioning and most states were never load-bearing. The plan is now two
-- columns on agent_goal, and human review of a plan is an ordinary
-- blocked(needs_plan_approval) state, reusing the existing block machinery.

-- Inline plan storage on the composite goal.
--   plan:       the DecompositionContent (children + edges); leaf goals stay '{}'.
--   planned_at: the materialize fence + "has been planned" flag; replaces the
--               accepted_revision_id pointer (planGateMet reads it now).
ALTER TABLE "agent_goal" ADD COLUMN "plan" jsonb NOT NULL DEFAULT '{}';
ALTER TABLE "agent_goal" ADD COLUMN "planned_at" timestamptz NULL;

-- Backfill: a materialized revision is the plan that built the live children.
-- Copy its content onto the goal and stamp planned_at from materialized_at so
-- the gate stays satisfied for already-decomposed composites.
UPDATE "agent_goal" g
SET "plan" = r."content",
    "planned_at" = r."materialized_at"
FROM "agent_goal_revision" r
WHERE r."goal_id" = g."id"
  AND r."materialized_at" IS NOT NULL;

-- Backfill: an open (draft/in_review) revision on a still-unplanned active
-- composite is a plan awaiting decision. Park the goal as
-- blocked(needs_plan_approval) carrying the proposed plan, so it lands in the
-- new human-review gate instead of being stranded with no revision row to act
-- on. planned_at stays NULL: it is not materialized yet.
UPDATE "agent_goal" g
SET "plan" = r."content",
    "lifecycle" = 'blocked',
    "block_reason" = 'needs_plan_approval'
FROM "agent_goal_revision" r
WHERE r."goal_id" = g."id"
  AND r."status" IN ('draft', 'in_review')
  AND g."planned_at" IS NULL
  AND g."lifecycle" = 'active';

-- Drop the accepted_revision_id pointer (replaced by planned_at) and its FK/index.
ALTER TABLE "agent_goal" DROP CONSTRAINT "agent_goal_accepted_revision_id_fkey";
DROP INDEX "idx_agent_goal_accepted_revision";
ALTER TABLE "agent_goal" DROP COLUMN "accepted_revision_id";

-- Drop the attempt->revision link (a decomposition attempt now writes goal.plan
-- directly; the attempt is reachable from the goal by purpose+status).
ALTER TABLE "agent_goal_attempt" DROP CONSTRAINT "agent_goal_attempt_revision_id_fkey";
ALTER TABLE "agent_goal_attempt" DROP COLUMN "revision_id";

-- Drop the revision table (its indexes and FKs go with it).
DROP TABLE "agent_goal_revision";

-- +goose Down
-- Irreversible consolidation: the agent_goal_revision rows (plan history,
-- per-version review state) are dropped and not recoverable from the inlined
-- plan column. Mirroring the baseline's policy for irreversible migrations, Down
-- is an explicit no-op rather than a lossy partial reconstruction.
SELECT 1;
