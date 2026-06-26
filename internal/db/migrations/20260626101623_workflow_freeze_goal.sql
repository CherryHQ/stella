-- +goose Up
-- A workflow is a frozen goal: the decomposition plan of a once-accepted
-- composite, snapshotted so a later run skips the planner and materializes a
-- deterministic subtree. The reproducible SPEC lives here; the reproducible
-- RESULT does not (deterministic checks still re-run in a new env).
CREATE TABLE "agent_workflow" (
    "id"                  uuid NOT NULL DEFAULT uuidv7(),
    "owner_kind"          text NOT NULL DEFAULT 'user',
    "user_id"             uuid NULL REFERENCES "auth_user"("id") ON DELETE CASCADE,
    "agent_id"            text NULL REFERENCES "agent"("id") ON DELETE CASCADE,
    "name"                text NOT NULL,
    "intent"              text NOT NULL DEFAULT '',
    "acceptance_contract" jsonb NOT NULL DEFAULT '{}',
    "convergence_policy"  jsonb NOT NULL DEFAULT '{}',
    "plan"                jsonb NOT NULL DEFAULT '{}',
    "version"             bigint NOT NULL DEFAULT 1,
    "source_goal_id"      text NULL REFERENCES "agent_goal"("id") ON DELETE SET NULL,
    "workflow_key"        text NOT NULL DEFAULT '',
    "created_at"          timestamptz NOT NULL DEFAULT now(),
    "updated_at"          timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY ("id"),
    CONSTRAINT "agent_workflow_check" CHECK ((version >= 1)),
    -- A user workflow is owned by a (user, agent); a system preset is keyed by a
    -- non-empty workflow_key for late-binding subscription (follow-up).
    CONSTRAINT "agent_workflow_owner_check" CHECK (
        ((owner_kind = 'user')   AND (user_id IS NOT NULL) AND (agent_id IS NOT NULL))
        OR ((owner_kind = 'system') AND (workflow_key <> ''))
    )
);

-- List a user's workflows by name.
CREATE INDEX "idx_agent_workflow_owner" ON "agent_workflow" ("user_id", "name");
-- A system preset's workflow_key is unique across presets.
CREATE UNIQUE INDEX "idx_agent_workflow_key" ON "agent_workflow" ("workflow_key")
    WHERE owner_kind = 'system';

-- Scheduler dispatch routing: a fired job runs a chat turn (default) or
-- instantiates a workflow (payload carries {"workflow_id": ...}).
ALTER TABLE "sched_job" ADD COLUMN "dispatch_kind" text NOT NULL DEFAULT 'chat';

-- A workflow-dispatch run instantiates a goal tree; record its root so the run
-- links to the live tree (distinct from session_id, which keeps chat context).
ALTER TABLE "sched_job_run" ADD COLUMN "root_goal_id" text NULL;

-- +goose Down
DROP TABLE "agent_workflow";
ALTER TABLE "sched_job" DROP COLUMN "dispatch_kind";
ALTER TABLE "sched_job_run" DROP COLUMN "root_goal_id";
