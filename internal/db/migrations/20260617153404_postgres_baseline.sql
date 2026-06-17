-- Create "agent" table
CREATE TABLE "agent" (
  "id" text NOT NULL,
  "name" text NOT NULL,
  "model" text NOT NULL DEFAULT '',
  "model_thinking" text NOT NULL DEFAULT '',
  "model_strong" text NOT NULL DEFAULT '',
  "model_strong_thinking" text NOT NULL DEFAULT '',
  "model_fast" text NOT NULL DEFAULT '',
  "model_fast_thinking" text NOT NULL DEFAULT '',
  "system_prompt" text NOT NULL DEFAULT '',
  "soul" text NOT NULL DEFAULT '',
  "workspace" text NOT NULL,
  "sandbox" text NOT NULL DEFAULT '{}',
  "enabled_builtin_skills" text NOT NULL DEFAULT '[]',
  "scope" text NOT NULL DEFAULT 'system',
  "creator_id" text NOT NULL DEFAULT '',
  "enabled" boolean NOT NULL DEFAULT true,
  "created_at" timestamptz NOT NULL DEFAULT now(),
  "updated_at" timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY ("id")
);
-- Create "agent_goal" table
CREATE TABLE "agent_goal" (
  "id" text NOT NULL,
  "user_id" text NOT NULL,
  "agent_id" text NOT NULL,
  "project_id" text NULL,
  "title" text NOT NULL,
  "description" text NOT NULL DEFAULT '',
  "status" text NOT NULL DEFAULT 'draft',
  "priority" text NOT NULL DEFAULT 'routine',
  "review_policy" text NOT NULL DEFAULT 'none',
  "active_review_id" text NULL,
  "context" text NOT NULL DEFAULT '{}',
  "output" text NOT NULL DEFAULT '{}',
  "created_at" timestamptz NOT NULL DEFAULT now(),
  "updated_at" timestamptz NOT NULL DEFAULT now(),
  "completed_at" timestamptz NULL,
  "cancelled_at" timestamptz NULL,
  PRIMARY KEY ("id")
);
-- Create index "idx_agent_goal_agent_project" to table: "agent_goal"
CREATE INDEX "idx_agent_goal_agent_project" ON "agent_goal" ("agent_id", "project_id");
-- Create index "idx_agent_goal_planning_candidates" to table: "agent_goal"
CREATE INDEX "idx_agent_goal_planning_candidates" ON "agent_goal" ("priority" DESC, "created_at") WHERE (status = 'draft'::text);
-- Create index "idx_agent_goal_project" to table: "agent_goal"
CREATE INDEX "idx_agent_goal_project" ON "agent_goal" ("project_id");
-- Create index "idx_agent_goal_synthesis_candidates" to table: "agent_goal"
CREATE INDEX "idx_agent_goal_synthesis_candidates" ON "agent_goal" ("priority" DESC, "updated_at") WHERE ((status = 'running'::text) AND (review_policy <> 'none'::text));
-- Create index "idx_agent_goal_user_created" to table: "agent_goal"
CREATE INDEX "idx_agent_goal_user_created" ON "agent_goal" ("user_id", "created_at" DESC, "id" DESC);
-- Create "agent_review" table
CREATE TABLE "agent_review" (
  "id" text NOT NULL,
  "task_id" text NULL,
  "goal_id" text NULL,
  "submitted_run_id" text NULL,
  "reviewer_run_id" text NULL,
  "reviewer_type" text NOT NULL,
  "reviewer_user_id" text NULL,
  "escalated_from_review_id" text NULL,
  "status" text NOT NULL DEFAULT 'requested',
  "summary" text NOT NULL DEFAULT '',
  "feedback" text NOT NULL DEFAULT '',
  "created_at" timestamptz NOT NULL DEFAULT now(),
  "updated_at" timestamptz NOT NULL DEFAULT now(),
  "resolved_at" timestamptz NULL,
  PRIMARY KEY ("id"),
  CONSTRAINT "agent_review_escalated_from_review_id_fkey" FOREIGN KEY ("escalated_from_review_id") REFERENCES "agent_review" ("id") ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT "agent_review_check" CHECK (((task_id IS NOT NULL) AND (goal_id IS NULL)) OR ((task_id IS NULL) AND (goal_id IS NOT NULL)))
);
-- Create index "idx_agent_review_open" to table: "agent_review"
CREATE INDEX "idx_agent_review_open" ON "agent_review" ("status") WHERE (status = ANY (ARRAY['requested'::text, 'in_progress'::text]));
-- Create index "idx_agent_review_task" to table: "agent_review"
CREATE INDEX "idx_agent_review_task" ON "agent_review" ("task_id", "created_at" DESC);
-- Create index "uniq_open_review_per_goal" to table: "agent_review"
CREATE UNIQUE INDEX "uniq_open_review_per_goal" ON "agent_review" ("goal_id") WHERE ((goal_id IS NOT NULL) AND (status = ANY (ARRAY['requested'::text, 'in_progress'::text])));
-- Create index "uniq_open_review_per_task" to table: "agent_review"
CREATE UNIQUE INDEX "uniq_open_review_per_task" ON "agent_review" ("task_id") WHERE ((task_id IS NOT NULL) AND (status = ANY (ARRAY['requested'::text, 'in_progress'::text])));
-- Create "agent_review_item" table
CREATE TABLE "agent_review_item" (
  "id" text NOT NULL,
  "review_id" text NOT NULL,
  "criterion_id" text NULL,
  "passed" boolean NULL,
  "evidence" text NOT NULL DEFAULT '',
  "created_at" timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY ("id")
);
-- Create index "idx_agent_review_item_review" to table: "agent_review_item"
CREATE INDEX "idx_agent_review_item_review" ON "agent_review_item" ("review_id");
-- Create "agent_task" table
CREATE TABLE "agent_task" (
  "id" text NOT NULL,
  "user_id" text NOT NULL,
  "agent_id" text NOT NULL,
  "session_id" text NOT NULL,
  "goal_id" text NULL,
  "project_id" text NULL,
  "title" text NOT NULL,
  "description" text NOT NULL DEFAULT '',
  "status" text NOT NULL DEFAULT 'draft',
  "priority" text NOT NULL DEFAULT 'routine',
  "review_policy" text NOT NULL DEFAULT 'none',
  "active_review_id" text NULL,
  "required" boolean NOT NULL DEFAULT true,
  "retry_count" bigint NOT NULL DEFAULT 0,
  "max_retries" bigint NOT NULL DEFAULT 3,
  "not_before" timestamptz NULL,
  "deadline_at" timestamptz NULL,
  "active_run_id" text NULL,
  "active_blocker_id" text NULL,
  "context" text NOT NULL DEFAULT '{}',
  "output" text NOT NULL DEFAULT '{}',
  "created_at" timestamptz NOT NULL DEFAULT now(),
  "updated_at" timestamptz NOT NULL DEFAULT now(),
  "completed_at" timestamptz NULL,
  "cancelled_at" timestamptz NULL,
  PRIMARY KEY ("id")
);
-- Create index "idx_agent_task_agent_status_not_before" to table: "agent_task"
CREATE INDEX "idx_agent_task_agent_status_not_before" ON "agent_task" ("agent_id", "status", "not_before");
-- Create index "idx_agent_task_goal" to table: "agent_task"
CREATE INDEX "idx_agent_task_goal" ON "agent_task" ("goal_id");
-- Create index "idx_agent_task_project" to table: "agent_task"
CREATE INDEX "idx_agent_task_project" ON "agent_task" ("project_id");
-- Create index "idx_agent_task_ready_candidates" to table: "agent_task"
CREATE INDEX "idx_agent_task_ready_candidates" ON "agent_task" ("priority" DESC, "created_at") WHERE ((status = 'ready'::text) AND (active_run_id IS NULL));
-- Create index "idx_agent_task_session" to table: "agent_task"
CREATE INDEX "idx_agent_task_session" ON "agent_task" ("session_id");
-- Create index "idx_agent_task_user_agent" to table: "agent_task"
CREATE INDEX "idx_agent_task_user_agent" ON "agent_task" ("user_id", "agent_id");
-- Create index "idx_agent_task_user_agent_project_created" to table: "agent_task"
CREATE INDEX "idx_agent_task_user_agent_project_created" ON "agent_task" ("user_id", "agent_id", "project_id", "created_at" DESC, "id" DESC);
-- Create index "idx_agent_task_user_agent_status_created" to table: "agent_task"
CREATE INDEX "idx_agent_task_user_agent_status_created" ON "agent_task" ("user_id", "agent_id", "status", "created_at" DESC, "id" DESC);
-- Create index "idx_agent_task_user_created" to table: "agent_task"
CREATE INDEX "idx_agent_task_user_created" ON "agent_task" ("user_id", "created_at" DESC, "id" DESC);
-- Create index "uniq_agent_task_session" to table: "agent_task"
CREATE UNIQUE INDEX "uniq_agent_task_session" ON "agent_task" ("session_id");
-- Create "agent_task_blocker" table
CREATE TABLE "agent_task_blocker" (
  "id" text NOT NULL,
  "task_id" text NOT NULL,
  "kind" text NOT NULL,
  "status" text NOT NULL DEFAULT 'open',
  "question" text NOT NULL DEFAULT '',
  "detail" text NOT NULL DEFAULT '{}',
  "resolution" text NOT NULL DEFAULT '{}',
  "created_by_run_id" text NULL,
  "created_at" timestamptz NOT NULL DEFAULT now(),
  "resolved_at" timestamptz NULL,
  PRIMARY KEY ("id")
);
-- Create index "idx_agent_task_blocker_task_open" to table: "agent_task_blocker"
CREATE INDEX "idx_agent_task_blocker_task_open" ON "agent_task_blocker" ("task_id") WHERE (status = 'open'::text);
-- Create index "uniq_open_blocker_per_task" to table: "agent_task_blocker"
CREATE UNIQUE INDEX "uniq_open_blocker_per_task" ON "agent_task_blocker" ("task_id") WHERE (status = 'open'::text);
-- Create "agent_task_criterion" table
CREATE TABLE "agent_task_criterion" (
  "id" text NOT NULL,
  "task_id" text NOT NULL,
  "description" text NOT NULL,
  "required_flag" boolean NOT NULL DEFAULT true,
  "position" bigint NOT NULL DEFAULT 0,
  "created_at" timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY ("id")
);
-- Create index "idx_agent_task_criterion_task" to table: "agent_task_criterion"
CREATE INDEX "idx_agent_task_criterion_task" ON "agent_task_criterion" ("task_id", "position");
-- Create "agent_task_dep" table
CREATE TABLE "agent_task_dep" (
  "task_id" text NOT NULL,
  "dep_task_id" text NOT NULL,
  "dep_kind" text NOT NULL DEFAULT 'hard',
  "on_failure" text NOT NULL DEFAULT 'block',
  "waived_at" timestamptz NULL,
  "waived_by_user" text NULL,
  "waiver_reason" text NOT NULL DEFAULT '',
  "created_at" timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY ("task_id", "dep_task_id"),
  CONSTRAINT "agent_task_dep_check" CHECK (task_id <> dep_task_id)
);
-- Create index "idx_agent_task_dep_dep" to table: "agent_task_dep"
CREATE INDEX "idx_agent_task_dep_dep" ON "agent_task_dep" ("dep_task_id");
-- Create "agent_task_dispatch_hint" table
CREATE TABLE "agent_task_dispatch_hint" (
  "id" text NOT NULL,
  "task_id" text NULL,
  "goal_id" text NULL,
  "kind" text NOT NULL,
  "executor_agent_id" text NOT NULL,
  "consumed_at" timestamptz NULL,
  "created_at" timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY ("id"),
  CONSTRAINT "agent_task_dispatch_hint_check" CHECK (((task_id IS NOT NULL) AND (goal_id IS NULL) AND (kind = ANY (ARRAY['worker'::text, 'reviewer'::text]))) OR ((task_id IS NULL) AND (goal_id IS NOT NULL) AND (kind = ANY (ARRAY['planner'::text, 'synthesizer'::text]))))
);
-- Create index "uniq_active_dispatch_hint_goal" to table: "agent_task_dispatch_hint"
CREATE UNIQUE INDEX "uniq_active_dispatch_hint_goal" ON "agent_task_dispatch_hint" ("goal_id", "kind") WHERE ((goal_id IS NOT NULL) AND (consumed_at IS NULL));
-- Create index "uniq_active_dispatch_hint_task" to table: "agent_task_dispatch_hint"
CREATE UNIQUE INDEX "uniq_active_dispatch_hint_task" ON "agent_task_dispatch_hint" ("task_id", "kind") WHERE ((task_id IS NOT NULL) AND (consumed_at IS NULL));
-- Create "agent_task_event" table
CREATE TABLE "agent_task_event" (
  "id" text NOT NULL,
  "task_id" text NULL,
  "goal_id" text NULL,
  "run_id" text NULL,
  "blocker_id" text NULL,
  "review_id" text NULL,
  "event_type" text NOT NULL,
  "from_status" text NULL,
  "to_status" text NULL,
  "actor_type" text NOT NULL,
  "actor_id" text NULL,
  "detail" text NOT NULL DEFAULT '{}',
  "created_at" timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY ("id")
);
-- Create index "idx_agent_task_event_goal" to table: "agent_task_event"
CREATE INDEX "idx_agent_task_event_goal" ON "agent_task_event" ("goal_id", "created_at" DESC);
-- Create index "idx_agent_task_event_run" to table: "agent_task_event"
CREATE INDEX "idx_agent_task_event_run" ON "agent_task_event" ("run_id");
-- Create index "idx_agent_task_event_task" to table: "agent_task_event"
CREATE INDEX "idx_agent_task_event_task" ON "agent_task_event" ("task_id", "created_at" DESC);
-- Create "agent_task_run" table
CREATE TABLE "agent_task_run" (
  "id" text NOT NULL,
  "task_id" text NULL,
  "goal_id" text NULL,
  "user_id" text NOT NULL,
  "agent_id" text NULL,
  "executor_agent_id" text NULL,
  "kind" text NOT NULL DEFAULT 'worker',
  "attempt_no" bigint NOT NULL DEFAULT 1,
  "status" text NOT NULL DEFAULT 'queued',
  "session_id" text NOT NULL,
  "input" text NOT NULL DEFAULT '{}',
  "result" text NOT NULL DEFAULT '{}',
  "error" text NOT NULL DEFAULT '',
  "heartbeat_at" timestamptz NULL,
  "lease_expires_at" timestamptz NULL,
  "worker_id" text NOT NULL DEFAULT '',
  "started_at" timestamptz NULL,
  "finished_at" timestamptz NULL,
  "created_at" timestamptz NOT NULL DEFAULT now(),
  "updated_at" timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY ("id"),
  CONSTRAINT "agent_task_run_check" CHECK (((task_id IS NOT NULL) AND (goal_id IS NULL) AND (kind = ANY (ARRAY['worker'::text, 'reviewer'::text]))) OR ((task_id IS NULL) AND (goal_id IS NOT NULL) AND (kind = ANY (ARRAY['planner'::text, 'synthesizer'::text]))))
);
-- Create index "idx_agent_task_run_active" to table: "agent_task_run"
CREATE INDEX "idx_agent_task_run_active" ON "agent_task_run" ("status") WHERE (status = ANY (ARRAY['queued'::text, 'running'::text]));
-- Create index "idx_agent_task_run_goal" to table: "agent_task_run"
CREATE INDEX "idx_agent_task_run_goal" ON "agent_task_run" ("goal_id");
-- Create index "idx_agent_task_run_lease" to table: "agent_task_run"
CREATE INDEX "idx_agent_task_run_lease" ON "agent_task_run" ("lease_expires_at") WHERE (status = ANY (ARRAY['queued'::text, 'running'::text]));
-- Create index "idx_agent_task_run_task" to table: "agent_task_run"
CREATE INDEX "idx_agent_task_run_task" ON "agent_task_run" ("task_id", "attempt_no" DESC);
-- Create index "uniq_active_planner_run" to table: "agent_task_run"
CREATE UNIQUE INDEX "uniq_active_planner_run" ON "agent_task_run" ("goal_id") WHERE ((goal_id IS NOT NULL) AND (kind = 'planner'::text) AND (status = ANY (ARRAY['queued'::text, 'running'::text])));
-- Create index "uniq_active_reviewer_run" to table: "agent_task_run"
CREATE UNIQUE INDEX "uniq_active_reviewer_run" ON "agent_task_run" ("task_id") WHERE ((task_id IS NOT NULL) AND (kind = 'reviewer'::text) AND (status = ANY (ARRAY['queued'::text, 'running'::text])));
-- Create index "uniq_active_synthesizer_run" to table: "agent_task_run"
CREATE UNIQUE INDEX "uniq_active_synthesizer_run" ON "agent_task_run" ("goal_id") WHERE ((goal_id IS NOT NULL) AND (kind = 'synthesizer'::text) AND (status = ANY (ARRAY['queued'::text, 'running'::text])));
-- Create index "uniq_active_worker_run" to table: "agent_task_run"
CREATE UNIQUE INDEX "uniq_active_worker_run" ON "agent_task_run" ("task_id") WHERE ((task_id IS NOT NULL) AND (kind = 'worker'::text) AND (status = ANY (ARRAY['queued'::text, 'running'::text])));
-- Create index "uniq_goal_run_attempt" to table: "agent_task_run"
CREATE UNIQUE INDEX "uniq_goal_run_attempt" ON "agent_task_run" ("goal_id", "kind", "attempt_no") WHERE (goal_id IS NOT NULL);
-- Create index "uniq_task_run_attempt" to table: "agent_task_run"
CREATE UNIQUE INDEX "uniq_task_run_attempt" ON "agent_task_run" ("task_id", "kind", "attempt_no") WHERE (task_id IS NOT NULL);
-- Create "app_setting" table
CREATE TABLE "app_setting" (
  "key" text NOT NULL,
  "value" text NOT NULL DEFAULT '{}',
  "created_at" timestamptz NOT NULL DEFAULT now(),
  "updated_at" timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY ("key")
);
-- Create "auth_credential" table
CREATE TABLE "auth_credential" (
  "id" text NOT NULL,
  "user_id" text NOT NULL,
  "password_hash" text NOT NULL,
  "created_at" timestamptz NOT NULL DEFAULT now(),
  "updated_at" timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY ("id"),
  CONSTRAINT "auth_credential_user_id_key" UNIQUE ("user_id")
);
-- Create index "idx_auth_credential_user_id" to table: "auth_credential"
CREATE INDEX "idx_auth_credential_user_id" ON "auth_credential" ("user_id");
-- Create "auth_identity" table
CREATE TABLE "auth_identity" (
  "id" text NOT NULL,
  "user_id" text NOT NULL,
  "provider" text NOT NULL,
  "provider_subject" text NOT NULL,
  "email" text NOT NULL DEFAULT '',
  "name" text NOT NULL DEFAULT '',
  "avatar_url" text NOT NULL DEFAULT '',
  "raw_claims" text NOT NULL DEFAULT '{}',
  "created_at" timestamptz NOT NULL DEFAULT now(),
  "updated_at" timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY ("id"),
  CONSTRAINT "auth_identity_provider_provider_subject_key" UNIQUE ("provider", "provider_subject")
);
-- Create index "idx_auth_identity_user_id" to table: "auth_identity"
CREATE INDEX "idx_auth_identity_user_id" ON "auth_identity" ("user_id");
-- Create "auth_policy" table
CREATE TABLE "auth_policy" (
  "id" text NOT NULL,
  "name" text NOT NULL,
  "effect" text NOT NULL,
  "subjects" text NOT NULL DEFAULT '{}',
  "actions" text NOT NULL DEFAULT '[]',
  "resources" text NOT NULL DEFAULT '[]',
  "conditions" text NOT NULL DEFAULT '{}',
  "priority" bigint NOT NULL DEFAULT 0,
  "is_system" boolean NOT NULL DEFAULT false,
  "enabled" boolean NOT NULL DEFAULT true,
  "created_at" timestamptz NOT NULL DEFAULT now(),
  "updated_at" timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY ("id"),
  CONSTRAINT "auth_policy_effect_check" CHECK (effect = ANY (ARRAY['allow'::text, 'deny'::text]))
);
-- Create "auth_session" table
CREATE TABLE "auth_session" (
  "id" text NOT NULL,
  "user_id" text NOT NULL,
  "token_hash" text NOT NULL,
  "expires_at" timestamptz NOT NULL,
  "created_at" timestamptz NOT NULL DEFAULT now(),
  "updated_at" timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY ("id"),
  CONSTRAINT "auth_session_token_hash_key" UNIQUE ("token_hash")
);
-- Create index "idx_auth_session_token_hash" to table: "auth_session"
CREATE INDEX "idx_auth_session_token_hash" ON "auth_session" ("token_hash");
-- Create index "idx_auth_session_user_id" to table: "auth_session"
CREATE INDEX "idx_auth_session_user_id" ON "auth_session" ("user_id");
-- Create "auth_user" table
CREATE TABLE "auth_user" (
  "id" text NOT NULL,
  "email" text NOT NULL,
  "name" text NOT NULL DEFAULT '',
  "avatar_url" text NOT NULL DEFAULT '',
  "role" text NOT NULL DEFAULT 'user',
  "is_active" boolean NOT NULL DEFAULT true,
  "default_agent_id" text NULL,
  "notify_identity_id" text NULL,
  "age_public_key" text NOT NULL DEFAULT '',
  "age_private_key" text NOT NULL DEFAULT '',
  "created_at" timestamptz NOT NULL DEFAULT now(),
  "updated_at" timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY ("id"),
  CONSTRAINT "auth_user_email_key" UNIQUE ("email")
);
-- Create index "idx_auth_user_email" to table: "auth_user"
CREATE INDEX "idx_auth_user_email" ON "auth_user" ("email");
-- Create "auth_user_agent" table
CREATE TABLE "auth_user_agent" (
  "user_id" text NOT NULL,
  "agent_id" text NOT NULL,
  PRIMARY KEY ("user_id", "agent_id")
);
-- Create "auth_user_token" table
CREATE TABLE "auth_user_token" (
  "id" text NOT NULL,
  "user_id" text NOT NULL,
  "name" text NOT NULL DEFAULT '',
  "token_hash" text NOT NULL,
  "token_prefix" text NOT NULL DEFAULT '',
  "auto_generated" boolean NOT NULL DEFAULT false,
  "last_used_at" timestamptz NULL,
  "expires_at" timestamptz NULL,
  "rotated_at" timestamptz NULL,
  "revoked_at" timestamptz NULL,
  "created_at" timestamptz NOT NULL DEFAULT now(),
  "updated_at" timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY ("id"),
  CONSTRAINT "auth_user_token_token_hash_key" UNIQUE ("token_hash")
);
-- Create index "idx_auth_user_token_auto_active" to table: "auth_user_token"
CREATE UNIQUE INDEX "idx_auth_user_token_auto_active" ON "auth_user_token" ("user_id") WHERE ((auto_generated = true) AND (revoked_at IS NULL));
-- Create "channel" table
CREATE TABLE "channel" (
  "id" text NOT NULL,
  "name" text NOT NULL DEFAULT '',
  "type" text NOT NULL DEFAULT '',
  "agent_id" text NULL,
  "enabled" boolean NOT NULL DEFAULT true,
  "config" text NOT NULL DEFAULT '{}',
  "created_at" timestamptz NOT NULL DEFAULT now(),
  "updated_at" timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY ("id")
);
-- Create "channel_agent" table
CREATE TABLE "channel_agent" (
  "channel_id" text NOT NULL DEFAULT '',
  "platform" text NOT NULL,
  "chat_id" text NOT NULL,
  "agent_id" text NOT NULL,
  "created_at" timestamptz NOT NULL DEFAULT now(),
  "updated_at" timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY ("channel_id", "platform", "chat_id")
);
-- Create "channel_group_member" table
CREATE TABLE "channel_group_member" (
  "group_id" text NOT NULL,
  "agent_id" text NOT NULL,
  "reply_channel_id" text NOT NULL,
  "created_at" timestamptz NOT NULL DEFAULT now(),
  "updated_at" timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY ("group_id", "agent_id")
);
-- Create "channel_identity" table
CREATE TABLE "channel_identity" (
  "id" text NOT NULL,
  "user_id" text NOT NULL,
  "platform" text NOT NULL,
  "external_id" text NOT NULL,
  "name" text NOT NULL DEFAULT '',
  "created_at" timestamptz NOT NULL DEFAULT now(),
  "updated_at" timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY ("id"),
  CONSTRAINT "channel_identity_platform_external_id_key" UNIQUE ("platform", "external_id")
);
-- Create index "idx_channel_identity_user_id" to table: "channel_identity"
CREATE INDEX "idx_channel_identity_user_id" ON "channel_identity" ("user_id");
-- Create "ctx_agent_memory" table
CREATE TABLE "ctx_agent_memory" (
  "user_id" text NOT NULL,
  "agent_id" text NOT NULL,
  "content" text NOT NULL DEFAULT '',
  "soul" text NOT NULL DEFAULT '',
  "version" bigint NOT NULL DEFAULT 0,
  "constraints" text NOT NULL DEFAULT '[]',
  "profile_entries" text NOT NULL DEFAULT '[]',
  "created_at" timestamptz NOT NULL DEFAULT now(),
  "updated_at" timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY ("user_id", "agent_id")
);
-- Create "ctx_agent_memory_changelog" table
CREATE TABLE "ctx_agent_memory_changelog" (
  "id" text NOT NULL,
  "user_id" text NOT NULL,
  "agent_id" text NOT NULL,
  "session_id" text NULL,
  "entity_id" text NULL,
  "scope" text NOT NULL,
  "action" text NOT NULL,
  "source" text NOT NULL,
  "memory_version_before" bigint NULL,
  "memory_version_after" bigint NULL,
  "before_text" text NULL,
  "after_text" text NULL,
  "metadata" text NULL,
  "created_at" timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY ("id")
);
-- Create index "idx_ctx_agent_memory_changelog_created" to table: "ctx_agent_memory_changelog"
CREATE INDEX "idx_ctx_agent_memory_changelog_created" ON "ctx_agent_memory_changelog" ("created_at");
-- Create index "idx_ctx_agent_memory_changelog_session" to table: "ctx_agent_memory_changelog"
CREATE INDEX "idx_ctx_agent_memory_changelog_session" ON "ctx_agent_memory_changelog" ("session_id");
-- Create index "idx_ctx_agent_memory_changelog_user_agent" to table: "ctx_agent_memory_changelog"
CREATE INDEX "idx_ctx_agent_memory_changelog_user_agent" ON "ctx_agent_memory_changelog" ("user_id", "agent_id", "scope");
-- Create index "idx_ctx_agent_memory_changelog_version" to table: "ctx_agent_memory_changelog"
CREATE INDEX "idx_ctx_agent_memory_changelog_version" ON "ctx_agent_memory_changelog" ("user_id", "agent_id", "scope", "memory_version_after");
-- Create "ctx_agent_memory_snapshot" table
CREATE TABLE "ctx_agent_memory_snapshot" (
  "session_id" text NOT NULL,
  "user_id" text NOT NULL,
  "agent_id" text NOT NULL,
  "version" bigint NOT NULL DEFAULT 0,
  "created_at" timestamptz NOT NULL DEFAULT now(),
  "updated_at" timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY ("session_id", "user_id", "agent_id")
);
-- Create index "idx_ctx_agent_memory_snapshot_user_agent" to table: "ctx_agent_memory_snapshot"
CREATE INDEX "idx_ctx_agent_memory_snapshot_user_agent" ON "ctx_agent_memory_snapshot" ("user_id", "agent_id");
-- Create "ctx_conversation" table
CREATE TABLE "ctx_conversation" (
  "id" text NOT NULL,
  "session_id" text NOT NULL,
  "title" text NULL,
  "channel" text NOT NULL DEFAULT '',
  "kind" text NOT NULL DEFAULT 'chat',
  "project_id" text NULL,
  "archived" boolean NOT NULL DEFAULT false,
  "last_active" timestamptz NOT NULL DEFAULT now(),
  "bootstrapped_at" timestamptz NULL,
  "agent_id" text NULL,
  "user_id" text NULL,
  "created_at" timestamptz NOT NULL DEFAULT now(),
  "updated_at" timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY ("id"),
  CONSTRAINT "ctx_conversation_session_id_key" UNIQUE ("session_id")
);
-- Create index "idx_ctx_conversation_review_agent_active" to table: "ctx_conversation"
CREATE INDEX "idx_ctx_conversation_review_agent_active" ON "ctx_conversation" ("agent_id", "archived", "last_active" DESC);
-- Create index "idx_ctx_conversation_user_agent_active" to table: "ctx_conversation"
CREATE INDEX "idx_ctx_conversation_user_agent_active" ON "ctx_conversation" ("user_id", "agent_id", "archived", "last_active" DESC);
-- Create index "idx_ctx_conversation_user_agent_kind_active" to table: "ctx_conversation"
CREATE INDEX "idx_ctx_conversation_user_agent_kind_active" ON "ctx_conversation" ("user_id", "agent_id", "kind", "archived", "last_active" DESC);
-- Create index "idx_one_agent_main" to table: "ctx_conversation"
CREATE UNIQUE INDEX "idx_one_agent_main" ON "ctx_conversation" ("agent_id", "user_id") WHERE ((kind = 'main'::text) AND (project_id IS NULL) AND (archived = false));
-- Create index "idx_one_project_main" to table: "ctx_conversation"
CREATE UNIQUE INDEX "idx_one_project_main" ON "ctx_conversation" ("project_id") WHERE ((kind = 'main'::text) AND (project_id IS NOT NULL) AND (archived = false));
-- Create "ctx_group_dispatch" table
CREATE TABLE "ctx_group_dispatch" (
  "id" text NOT NULL,
  "group_message_id" text NOT NULL,
  "group_id" text NOT NULL,
  "agent_id" text NOT NULL,
  "reply_channel_id" text NOT NULL,
  "status" text NOT NULL DEFAULT 'pending',
  "attempt_count" bigint NOT NULL DEFAULT 0,
  "lease_until" timestamptz NULL,
  "next_attempt_at" timestamptz NULL,
  "last_error" text NOT NULL DEFAULT '',
  "result_message_id" text NOT NULL DEFAULT '',
  "created_at" timestamptz NOT NULL DEFAULT now(),
  "updated_at" timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY ("id"),
  CONSTRAINT "ctx_group_dispatch_group_message_id_agent_id_key" UNIQUE ("group_message_id", "agent_id")
);
-- Create index "idx_ctx_group_dispatch_group_id" to table: "ctx_group_dispatch"
CREATE INDEX "idx_ctx_group_dispatch_group_id" ON "ctx_group_dispatch" ("group_id");
-- Create index "idx_ctx_group_dispatch_pending" to table: "ctx_group_dispatch"
CREATE INDEX "idx_ctx_group_dispatch_pending" ON "ctx_group_dispatch" ("status", "next_attempt_at", "created_at") WHERE (status = 'pending'::text);
-- Create index "idx_ctx_group_dispatch_reply_channel" to table: "ctx_group_dispatch"
CREATE INDEX "idx_ctx_group_dispatch_reply_channel" ON "ctx_group_dispatch" ("reply_channel_id");
-- Create index "idx_ctx_group_dispatch_running_lease" to table: "ctx_group_dispatch"
CREATE INDEX "idx_ctx_group_dispatch_running_lease" ON "ctx_group_dispatch" ("status", "lease_until") WHERE (status = 'running'::text);
-- Create "ctx_group_ingest_cursor" table
CREATE TABLE "ctx_group_ingest_cursor" (
  "group_id" text NOT NULL,
  "pipeline" text NOT NULL DEFAULT 'memory_ingest',
  "last_seq" bigint NOT NULL DEFAULT 0,
  "updated_at" timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY ("group_id", "pipeline")
);
-- Create "ctx_group_ingest_error" table
CREATE TABLE "ctx_group_ingest_error" (
  "id" text NOT NULL,
  "group_id" text NOT NULL,
  "pipeline" text NOT NULL,
  "seq" bigint NOT NULL,
  "reason" text NOT NULL DEFAULT '',
  "created_at" timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY ("id")
);
-- Create index "idx_group_ingest_error_dedup" to table: "ctx_group_ingest_error"
CREATE UNIQUE INDEX "idx_group_ingest_error_dedup" ON "ctx_group_ingest_error" ("group_id", "pipeline", "seq");
-- Create "ctx_group_memory" table
CREATE TABLE "ctx_group_memory" (
  "group_id" text NOT NULL,
  "content" text NOT NULL DEFAULT '',
  "version" bigint NOT NULL DEFAULT 0,
  "created_at" timestamptz NOT NULL DEFAULT now(),
  "updated_at" timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY ("group_id")
);
-- Create "ctx_group_message" table
CREATE TABLE "ctx_group_message" (
  "id" text NOT NULL,
  "group_id" text NOT NULL,
  "seq" bigint NOT NULL,
  "source_channel_id" text NULL,
  "actor_type" text NOT NULL,
  "actor_id" text NOT NULL,
  "platform_message_id" text NULL,
  "reply_to" text NULL,
  "platform_timestamp" timestamptz NULL,
  "idempotency_key" text NULL,
  "content" text NOT NULL DEFAULT '',
  "reasoning" text NOT NULL DEFAULT '',
  "agent_session_id" text NOT NULL DEFAULT '',
  "created_at" timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY ("id"),
  CONSTRAINT "ctx_group_message_group_id_seq_key" UNIQUE ("group_id", "seq")
);
-- Create index "idx_ctx_group_message_idem" to table: "ctx_group_message"
CREATE UNIQUE INDEX "idx_ctx_group_message_idem" ON "ctx_group_message" ("idempotency_key") WHERE (idempotency_key IS NOT NULL);
-- Create index "idx_ctx_group_message_platform_msg" to table: "ctx_group_message"
CREATE UNIQUE INDEX "idx_ctx_group_message_platform_msg" ON "ctx_group_message" ("group_id", "platform_message_id") WHERE ((platform_message_id IS NOT NULL) AND (platform_message_id <> ''::text));
-- Create "ctx_group_outbox" table
CREATE TABLE "ctx_group_outbox" (
  "id" text NOT NULL,
  "group_message_id" text NOT NULL,
  "group_id" text NOT NULL,
  "envelope" text NOT NULL DEFAULT '{}',
  "status" text NOT NULL DEFAULT 'pending',
  "attempt_count" bigint NOT NULL DEFAULT 0,
  "lease_until" timestamptz NULL,
  "next_attempt_at" timestamptz NULL,
  "last_error" text NOT NULL DEFAULT '',
  "created_at" timestamptz NOT NULL DEFAULT now(),
  "updated_at" timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY ("id"),
  CONSTRAINT "ctx_group_outbox_group_message_id_key" UNIQUE ("group_message_id")
);
-- Create index "idx_ctx_group_outbox_group_id" to table: "ctx_group_outbox"
CREATE INDEX "idx_ctx_group_outbox_group_id" ON "ctx_group_outbox" ("group_id");
-- Create index "idx_ctx_group_outbox_pending" to table: "ctx_group_outbox"
CREATE INDEX "idx_ctx_group_outbox_pending" ON "ctx_group_outbox" ("status", "next_attempt_at", "created_at") WHERE (status = 'pending'::text);
-- Create index "idx_ctx_group_outbox_running_lease" to table: "ctx_group_outbox"
CREATE INDEX "idx_ctx_group_outbox_running_lease" ON "ctx_group_outbox" ("status", "lease_until") WHERE (status = 'running'::text);
-- Create "ctx_group_state" table
CREATE TABLE "ctx_group_state" (
  "id" text NOT NULL,
  "platform" text NOT NULL,
  "platform_group_id" text NOT NULL,
  "platform_thread_id" text NOT NULL DEFAULT '',
  "next_seq" bigint NOT NULL DEFAULT 0,
  "created_at" timestamptz NOT NULL DEFAULT now(),
  "updated_at" timestamptz NOT NULL DEFAULT now(),
  "group_name" text NOT NULL DEFAULT '',
  "created_by_user_id" text NULL,
  PRIMARY KEY ("id"),
  CONSTRAINT "ctx_group_state_platform_platform_group_id_platform_thread__key" UNIQUE ("platform", "platform_group_id", "platform_thread_id")
);
-- Create index "idx_ctx_group_state_web_owner" to table: "ctx_group_state"
CREATE INDEX "idx_ctx_group_state_web_owner" ON "ctx_group_state" ("created_by_user_id") WHERE (platform = 'web'::text);
-- Create "ctx_item" table
CREATE TABLE "ctx_item" (
  "conversation_id" text NOT NULL,
  "ordinal" bigint NOT NULL,
  "item_type" text NOT NULL,
  "message_id" text NULL,
  "summary_id" text NULL,
  "event_type" text NOT NULL DEFAULT '',
  "role" text NOT NULL DEFAULT '',
  "created_at" timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY ("conversation_id", "ordinal"),
  CONSTRAINT "ctx_item_check" CHECK (((item_type = 'message'::text) AND (message_id IS NOT NULL) AND (summary_id IS NULL)) OR ((item_type = 'summary'::text) AND (summary_id IS NOT NULL) AND (message_id IS NULL)))
);
-- Create index "idx_ctx_item_conv" to table: "ctx_item"
CREATE INDEX "idx_ctx_item_conv" ON "ctx_item" ("conversation_id", "ordinal");
-- Create "ctx_message" table
CREATE TABLE "ctx_message" (
  "id" text NOT NULL,
  "conversation_id" text NOT NULL,
  "seq" bigint NOT NULL,
  "role" text NOT NULL,
  "event_type" text NOT NULL DEFAULT 'text',
  "content" text NOT NULL,
  "token_count" bigint NOT NULL,
  "created_at" timestamptz NOT NULL DEFAULT now(),
  "content_tsv" tsvector NULL GENERATED ALWAYS AS (to_tsvector('simple'::regconfig, content)) STORED,
  PRIMARY KEY ("id"),
  CONSTRAINT "ctx_message_conversation_id_seq_key" UNIQUE ("conversation_id", "seq")
);
-- Create index "idx_ctx_message_content_trgm" to table: "ctx_message"
CREATE INDEX "idx_ctx_message_content_trgm" ON "ctx_message" USING gin ("content" gin_trgm_ops);
-- Create index "idx_ctx_message_conv_seq" to table: "ctx_message"
CREATE INDEX "idx_ctx_message_conv_seq" ON "ctx_message" ("conversation_id", "seq");
-- Create index "idx_ctx_message_tsv" to table: "ctx_message"
CREATE INDEX "idx_ctx_message_tsv" ON "ctx_message" USING gin ("content_tsv");
-- Create "ctx_message_part" table
CREATE TABLE "ctx_message_part" (
  "id" text NOT NULL,
  "message_id" text NOT NULL,
  "part_type" text NOT NULL,
  "ordinal" bigint NOT NULL,
  "text_content" text NULL,
  "tool_call_id" text NULL,
  "tool_name" text NULL,
  "tool_input" text NULL,
  "tool_output" text NULL,
  "metadata" text NULL,
  PRIMARY KEY ("id"),
  CONSTRAINT "ctx_message_part_message_id_ordinal_key" UNIQUE ("message_id", "ordinal")
);
-- Create "ctx_summary" table
CREATE TABLE "ctx_summary" (
  "id" text NOT NULL,
  "conversation_id" text NOT NULL,
  "kind" text NOT NULL,
  "depth" bigint NOT NULL DEFAULT 0,
  "content" text NOT NULL,
  "token_count" bigint NOT NULL,
  "earliest_at" timestamptz NULL,
  "latest_at" timestamptz NULL,
  "descendant_count" bigint NOT NULL DEFAULT 0,
  "descendant_token_count" bigint NOT NULL DEFAULT 0,
  "source_message_token_count" bigint NOT NULL DEFAULT 0,
  "created_at" timestamptz NOT NULL DEFAULT now(),
  "content_tsv" tsvector NULL GENERATED ALWAYS AS (to_tsvector('simple'::regconfig, content)) STORED,
  PRIMARY KEY ("id")
);
-- Create index "idx_ctx_summary_content_trgm" to table: "ctx_summary"
CREATE INDEX "idx_ctx_summary_content_trgm" ON "ctx_summary" USING gin ("content" gin_trgm_ops);
-- Create index "idx_ctx_summary_conv" to table: "ctx_summary"
CREATE INDEX "idx_ctx_summary_conv" ON "ctx_summary" ("conversation_id", "created_at");
-- Create index "idx_ctx_summary_tsv" to table: "ctx_summary"
CREATE INDEX "idx_ctx_summary_tsv" ON "ctx_summary" USING gin ("content_tsv");
-- Create "ctx_summary_message" table
CREATE TABLE "ctx_summary_message" (
  "summary_id" text NOT NULL,
  "message_id" text NOT NULL,
  "ordinal" bigint NOT NULL,
  PRIMARY KEY ("summary_id", "message_id")
);
-- Create "ctx_summary_parent" table
CREATE TABLE "ctx_summary_parent" (
  "summary_id" text NOT NULL,
  "parent_summary_id" text NOT NULL,
  "ordinal" bigint NOT NULL,
  PRIMARY KEY ("summary_id", "parent_summary_id")
);
-- Create index "idx_ctx_summary_parent_parent" to table: "ctx_summary_parent"
CREATE INDEX "idx_ctx_summary_parent_parent" ON "ctx_summary_parent" ("parent_summary_id", "ordinal");
-- Create "plugin" table
CREATE TABLE "plugin" (
  "id" text NOT NULL,
  "kind" text NOT NULL,
  "name" text NOT NULL,
  "enabled" boolean NOT NULL DEFAULT true,
  "config" text NOT NULL DEFAULT '{}',
  "created_at" timestamptz NOT NULL DEFAULT now(),
  "updated_at" timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY ("id")
);
-- Create "plugin_oauth_provider" table
CREATE TABLE "plugin_oauth_provider" (
  "id" text NOT NULL,
  "provider_id" text NOT NULL,
  "client_id" text NOT NULL DEFAULT '',
  "client_secret_enc" text NOT NULL DEFAULT '',
  "redirect_url" text NOT NULL DEFAULT '',
  "created_at" timestamptz NOT NULL DEFAULT now(),
  "updated_at" timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY ("id"),
  CONSTRAINT "plugin_oauth_provider_provider_id_key" UNIQUE ("provider_id")
);
-- Create "plugin_override" table
CREATE TABLE "plugin_override" (
  "plugin_id" text NOT NULL,
  "enabled" boolean NULL,
  "session_env_vault_key" text NOT NULL DEFAULT '',
  "config" text NOT NULL DEFAULT '',
  "created_at" timestamptz NOT NULL DEFAULT now(),
  "updated_at" timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY ("plugin_id")
);
-- Create "plugin_state" table
CREATE TABLE "plugin_state" (
  "plugin_id" text NOT NULL,
  "scope_kind" text NOT NULL,
  "scope_id" text NOT NULL DEFAULT '',
  "state_key" text NOT NULL,
  "value" text NOT NULL DEFAULT '{}',
  "created_at" timestamptz NOT NULL DEFAULT now(),
  "updated_at" timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY ("plugin_id", "scope_kind", "scope_id", "state_key")
);
-- Create "project" table
CREATE TABLE "project" (
  "id" text NOT NULL,
  "agent_id" text NOT NULL,
  "user_id" text NOT NULL,
  "name" text NOT NULL,
  "base_dir" text NOT NULL,
  "description" text NULL,
  "archived" boolean NOT NULL DEFAULT false,
  "created_at" timestamptz NOT NULL DEFAULT now(),
  "updated_at" timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY ("id"),
  CONSTRAINT "project_agent_id_user_id_name_key" UNIQUE ("agent_id", "user_id", "name")
);
-- Create "provider" table
CREATE TABLE "provider" (
  "id" text NOT NULL,
  "type" text NOT NULL,
  "name" text NOT NULL,
  "enabled" boolean NOT NULL DEFAULT true,
  "config" text NOT NULL DEFAULT '{}',
  "created_at" timestamptz NOT NULL DEFAULT now(),
  "updated_at" timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY ("id")
);
-- Create "recally_article" table
CREATE TABLE "recally_article" (
  "id" text NOT NULL,
  "user_id" text NOT NULL,
  "agent_id" text NULL,
  "url" text NOT NULL,
  "canonical_url" text NOT NULL,
  "source_type" text NOT NULL DEFAULT 'web',
  "title" text NOT NULL DEFAULT '',
  "author" text NOT NULL DEFAULT '',
  "summary" text NOT NULL DEFAULT '',
  "tags" text NOT NULL DEFAULT '[]',
  "status" text NOT NULL DEFAULT 'unread',
  "starred" boolean NOT NULL DEFAULT false,
  "file_path" text NOT NULL DEFAULT '',
  "metadata" text NOT NULL DEFAULT '{}',
  "published_at" timestamptz NULL,
  "saved_at" timestamptz NOT NULL DEFAULT now(),
  "read_at" timestamptz NULL,
  "created_at" timestamptz NOT NULL DEFAULT now(),
  "updated_at" timestamptz NOT NULL DEFAULT now(),
  "search_tsv" tsvector NULL GENERATED ALWAYS AS (((setweight(to_tsvector('simple'::regconfig, COALESCE(title, ''::text)), 'A'::"char") || setweight(to_tsvector('simple'::regconfig, COALESCE(summary, ''::text)), 'B'::"char")) || setweight(to_tsvector('simple'::regconfig, COALESCE(tags, ''::text)), 'B'::"char")) || setweight(to_tsvector('simple'::regconfig, COALESCE(author, ''::text)), 'C'::"char")) STORED,
  PRIMARY KEY ("id")
);
-- Create index "idx_recally_article_author_trgm" to table: "recally_article"
CREATE INDEX "idx_recally_article_author_trgm" ON "recally_article" USING gin ("author" gin_trgm_ops);
-- Create index "idx_recally_article_saved_at" to table: "recally_article"
CREATE INDEX "idx_recally_article_saved_at" ON "recally_article" ("saved_at");
-- Create index "idx_recally_article_summary_trgm" to table: "recally_article"
CREATE INDEX "idx_recally_article_summary_trgm" ON "recally_article" USING gin ("summary" gin_trgm_ops);
-- Create index "idx_recally_article_tags_trgm" to table: "recally_article"
CREATE INDEX "idx_recally_article_tags_trgm" ON "recally_article" USING gin ("tags" gin_trgm_ops);
-- Create index "idx_recally_article_title_trgm" to table: "recally_article"
CREATE INDEX "idx_recally_article_title_trgm" ON "recally_article" USING gin ("title" gin_trgm_ops);
-- Create index "idx_recally_article_tsv" to table: "recally_article"
CREATE INDEX "idx_recally_article_tsv" ON "recally_article" USING gin ("search_tsv");
-- Create index "idx_recally_article_user_canonical" to table: "recally_article"
CREATE UNIQUE INDEX "idx_recally_article_user_canonical" ON "recally_article" ("user_id", "canonical_url");
-- Create index "idx_recally_article_user_source" to table: "recally_article"
CREATE INDEX "idx_recally_article_user_source" ON "recally_article" ("user_id", "source_type");
-- Create index "idx_recally_article_user_starred" to table: "recally_article"
CREATE INDEX "idx_recally_article_user_starred" ON "recally_article" ("user_id", "starred") WHERE (starred = true);
-- Create index "idx_recally_article_user_status" to table: "recally_article"
CREATE INDEX "idx_recally_article_user_status" ON "recally_article" ("user_id", "status");
-- Create "recally_digest" table
CREATE TABLE "recally_digest" (
  "id" text NOT NULL,
  "user_id" text NOT NULL,
  "date" text NOT NULL,
  "narrative" text NOT NULL DEFAULT '',
  "saved_yesterday_count" bigint NOT NULL DEFAULT 0,
  "unread_count" bigint NOT NULL DEFAULT 0,
  "read_count" bigint NOT NULL DEFAULT 0,
  "archived_count" bigint NOT NULL DEFAULT 0,
  "starred_count" bigint NOT NULL DEFAULT 0,
  "worth_revisiting_count" bigint NOT NULL DEFAULT 0,
  "total_articles" bigint NOT NULL DEFAULT 0,
  "top_tags" text NOT NULL DEFAULT '[]',
  "created_at" timestamptz NOT NULL DEFAULT now(),
  "updated_at" timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY ("id")
);
-- Create index "idx_recally_digest_user_date" to table: "recally_digest"
CREATE UNIQUE INDEX "idx_recally_digest_user_date" ON "recally_digest" ("user_id", "date");
-- Create index "idx_recally_digest_user_id" to table: "recally_digest"
CREATE INDEX "idx_recally_digest_user_id" ON "recally_digest" ("user_id");
-- Create "recally_digest_article" table
CREATE TABLE "recally_digest_article" (
  "digest_id" text NOT NULL,
  "article_id" text NOT NULL,
  "section" text NOT NULL,
  "position" bigint NOT NULL DEFAULT 0,
  PRIMARY KEY ("digest_id", "article_id", "section")
);
-- Create index "idx_recally_digest_article_digest" to table: "recally_digest_article"
CREATE INDEX "idx_recally_digest_article_digest" ON "recally_digest_article" ("digest_id");
-- Create "recally_feed" table
CREATE TABLE "recally_feed" (
  "id" text NOT NULL,
  "user_id" text NOT NULL,
  "agent_id" text NULL,
  "url" text NOT NULL,
  "kind" text NOT NULL DEFAULT 'rss',
  "metadata" text NOT NULL DEFAULT '{}',
  "title" text NOT NULL DEFAULT '',
  "description" text NOT NULL DEFAULT '',
  "check_interval" text NOT NULL DEFAULT '1h',
  "last_checked_at" timestamptz NULL,
  "last_etag" text NOT NULL DEFAULT '',
  "last_modified" text NOT NULL DEFAULT '',
  "enabled" boolean NOT NULL DEFAULT true,
  "created_at" timestamptz NOT NULL DEFAULT now(),
  "updated_at" timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY ("id")
);
-- Create index "idx_recally_feed_user_url" to table: "recally_feed"
CREATE UNIQUE INDEX "idx_recally_feed_user_url" ON "recally_feed" ("user_id", "url");
-- Create "recally_feed_entry" table
CREATE TABLE "recally_feed_entry" (
  "id" text NOT NULL,
  "feed_id" text NOT NULL,
  "guid" text NOT NULL,
  "url" text NOT NULL DEFAULT '',
  "title" text NOT NULL DEFAULT '',
  "status" text NOT NULL DEFAULT 'pending',
  "article_id" text NULL,
  "attempts" bigint NOT NULL DEFAULT 0,
  "error_msg" text NOT NULL DEFAULT '',
  "discovered_at" timestamptz NOT NULL DEFAULT now(),
  "processed_at" timestamptz NULL,
  PRIMARY KEY ("id")
);
-- Create index "idx_recally_feed_entry_feed_guid" to table: "recally_feed_entry"
CREATE UNIQUE INDEX "idx_recally_feed_entry_feed_guid" ON "recally_feed_entry" ("feed_id", "guid");
-- Create index "idx_recally_feed_entry_status" to table: "recally_feed_entry"
CREATE INDEX "idx_recally_feed_entry_status" ON "recally_feed_entry" ("status");
-- Create "sched_job" table
CREATE TABLE "sched_job" (
  "id" text NOT NULL,
  "owner_kind" text NOT NULL DEFAULT 'user',
  "exec_scope" text NOT NULL DEFAULT 'user',
  "plugin_id" text NOT NULL DEFAULT '',
  "job_key" text NOT NULL DEFAULT '',
  "runtime_name" text NOT NULL DEFAULT '',
  "name" text NOT NULL,
  "description" text NOT NULL DEFAULT '',
  "schedule_cron" text NOT NULL DEFAULT '',
  "schedule_every" text NOT NULL DEFAULT '',
  "schedule_at" text NOT NULL DEFAULT '',
  "message" text NOT NULL DEFAULT '',
  "payload" text NOT NULL DEFAULT '{}',
  "session_mode" text NOT NULL DEFAULT 'reuse',
  "enabled" boolean NOT NULL DEFAULT true,
  "agent_id" text NULL,
  "user_id" text NULL,
  "created_at" timestamptz NOT NULL DEFAULT now(),
  "updated_at" timestamptz NOT NULL DEFAULT now(),
  "last_run_at" timestamptz NULL,
  "last_error" text NOT NULL DEFAULT '',
  PRIMARY KEY ("id")
);
-- Create index "idx_sched_job_owner" to table: "sched_job"
CREATE INDEX "idx_sched_job_owner" ON "sched_job" ("owner_kind", "plugin_id", "job_key");
-- Create "sched_job_run" table
CREATE TABLE "sched_job_run" (
  "id" text NOT NULL,
  "job_id" text NOT NULL,
  "session_id" text NOT NULL DEFAULT '',
  "status" text NOT NULL DEFAULT 'running',
  "started_at" timestamptz NOT NULL DEFAULT now(),
  "finished_at" timestamptz NULL,
  "error" text NOT NULL DEFAULT '',
  "output" text NOT NULL DEFAULT '',
  "user_id" text NULL,
  PRIMARY KEY ("id")
);
-- Create index "idx_sched_job_run_job_id" to table: "sched_job_run"
CREATE INDEX "idx_sched_job_run_job_id" ON "sched_job_run" ("job_id", "started_at" DESC);
-- Create "share" table
CREATE TABLE "share" (
  "id" text NOT NULL,
  "token_hash" text NOT NULL,
  "user_id" text NOT NULL,
  "title" text NOT NULL,
  "media_type" text NOT NULL,
  "content" bytea NOT NULL,
  "expires_at" timestamptz NULL,
  "created_at" timestamptz NOT NULL DEFAULT now(),
  "updated_at" timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY ("id"),
  CONSTRAINT "share_token_hash_key" UNIQUE ("token_hash")
);
-- Create index "idx_share_user" to table: "share"
CREATE INDEX "idx_share_user" ON "share" ("user_id", "created_at" DESC);
-- Create "skill" table
CREATE TABLE "skill" (
  "id" text NOT NULL,
  "scope" text NOT NULL,
  "user_id" text NULL,
  "agent_id" text NULL,
  "name" text NOT NULL,
  "description" text NOT NULL,
  "status" text NOT NULL DEFAULT 'active',
  "disable_model_invocation" boolean NOT NULL DEFAULT false,
  "metadata" jsonb NOT NULL DEFAULT '{}',
  "created_at" timestamptz NOT NULL DEFAULT now(),
  "updated_at" timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY ("id"),
  CONSTRAINT "skill_check" CHECK (((scope = 'user'::text) AND (user_id IS NOT NULL) AND (agent_id IS NULL)) OR ((scope = 'user_agent'::text) AND (user_id IS NOT NULL) AND (agent_id IS NOT NULL)) OR ((scope = 'system'::text) AND (user_id IS NULL) AND (agent_id IS NULL)) OR ((scope = 'system_agent'::text) AND (user_id IS NULL) AND (agent_id IS NOT NULL)))
);
-- Create index "idx_skill_owner_name" to table: "skill"
CREATE UNIQUE INDEX "idx_skill_owner_name" ON "skill" ("name", "scope", (COALESCE(user_id, ''::text)), (COALESCE(agent_id, ''::text)));
-- Create index "idx_skill_visibility" to table: "skill"
CREATE INDEX "idx_skill_visibility" ON "skill" ("scope", "user_id", "agent_id");
-- Create "skill_file" table
CREATE TABLE "skill_file" (
  "skill_id" text NOT NULL,
  "path" text NOT NULL,
  "content" text NOT NULL,
  PRIMARY KEY ("skill_id", "path")
);
-- Create "vault_entry" table
CREATE TABLE "vault_entry" (
  "id" text NOT NULL,
  "scope" text NOT NULL DEFAULT 'user',
  "user_id" text NULL,
  "agent_id" text NULL,
  "name" text NOT NULL,
  "ciphertext" text NOT NULL,
  "created_at" timestamptz NOT NULL DEFAULT now(),
  "updated_at" timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY ("id"),
  CONSTRAINT "vault_entry_check" CHECK (((scope = 'user'::text) AND (user_id IS NOT NULL) AND (agent_id IS NULL)) OR ((scope = 'user_agent'::text) AND (user_id IS NOT NULL) AND (agent_id IS NOT NULL)) OR ((scope = 'system'::text) AND (user_id IS NULL) AND (agent_id IS NULL)) OR ((scope = 'system_agent'::text) AND (user_id IS NULL) AND (agent_id IS NOT NULL)))
);
-- Create index "idx_vault_entry_scope" to table: "vault_entry"
CREATE INDEX "idx_vault_entry_scope" ON "vault_entry" ("scope", "user_id", "agent_id");
-- Create index "uniq_vault_entry_scope_key" to table: "vault_entry"
CREATE UNIQUE INDEX "uniq_vault_entry_scope_key" ON "vault_entry" ("scope", (COALESCE(user_id, ''::text)), (COALESCE(agent_id, ''::text)), "name");
-- Modify "agent_goal" table
ALTER TABLE "agent_goal" ADD CONSTRAINT "agent_goal_active_review_id_fkey" FOREIGN KEY ("active_review_id") REFERENCES "agent_review" ("id") ON UPDATE NO ACTION ON DELETE RESTRICT, ADD CONSTRAINT "agent_goal_agent_id_fkey" FOREIGN KEY ("agent_id") REFERENCES "agent" ("id") ON UPDATE NO ACTION ON DELETE RESTRICT, ADD CONSTRAINT "agent_goal_project_id_fkey" FOREIGN KEY ("project_id") REFERENCES "project" ("id") ON UPDATE NO ACTION ON DELETE SET NULL, ADD CONSTRAINT "agent_goal_user_id_fkey" FOREIGN KEY ("user_id") REFERENCES "auth_user" ("id") ON UPDATE NO ACTION ON DELETE CASCADE;
-- Modify "agent_review" table
ALTER TABLE "agent_review" ADD CONSTRAINT "agent_review_goal_id_fkey" FOREIGN KEY ("goal_id") REFERENCES "agent_goal" ("id") ON UPDATE NO ACTION ON DELETE CASCADE, ADD CONSTRAINT "agent_review_reviewer_run_id_fkey" FOREIGN KEY ("reviewer_run_id") REFERENCES "agent_task_run" ("id") ON UPDATE NO ACTION ON DELETE SET NULL, ADD CONSTRAINT "agent_review_reviewer_user_id_fkey" FOREIGN KEY ("reviewer_user_id") REFERENCES "auth_user" ("id") ON UPDATE NO ACTION ON DELETE SET NULL, ADD CONSTRAINT "agent_review_submitted_run_id_fkey" FOREIGN KEY ("submitted_run_id") REFERENCES "agent_task_run" ("id") ON UPDATE NO ACTION ON DELETE SET NULL, ADD CONSTRAINT "agent_review_task_id_fkey" FOREIGN KEY ("task_id") REFERENCES "agent_task" ("id") ON UPDATE NO ACTION ON DELETE CASCADE;
-- Modify "agent_review_item" table
ALTER TABLE "agent_review_item" ADD CONSTRAINT "agent_review_item_criterion_id_fkey" FOREIGN KEY ("criterion_id") REFERENCES "agent_task_criterion" ("id") ON UPDATE NO ACTION ON DELETE SET NULL, ADD CONSTRAINT "agent_review_item_review_id_fkey" FOREIGN KEY ("review_id") REFERENCES "agent_review" ("id") ON UPDATE NO ACTION ON DELETE CASCADE;
-- Modify "agent_task" table
ALTER TABLE "agent_task" ADD CONSTRAINT "agent_task_active_blocker_id_fkey" FOREIGN KEY ("active_blocker_id") REFERENCES "agent_task_blocker" ("id") ON UPDATE NO ACTION ON DELETE RESTRICT, ADD CONSTRAINT "agent_task_active_review_id_fkey" FOREIGN KEY ("active_review_id") REFERENCES "agent_review" ("id") ON UPDATE NO ACTION ON DELETE RESTRICT, ADD CONSTRAINT "agent_task_active_run_id_fkey" FOREIGN KEY ("active_run_id") REFERENCES "agent_task_run" ("id") ON UPDATE NO ACTION ON DELETE RESTRICT, ADD CONSTRAINT "agent_task_agent_id_fkey" FOREIGN KEY ("agent_id") REFERENCES "agent" ("id") ON UPDATE NO ACTION ON DELETE RESTRICT, ADD CONSTRAINT "agent_task_goal_id_fkey" FOREIGN KEY ("goal_id") REFERENCES "agent_goal" ("id") ON UPDATE NO ACTION ON DELETE CASCADE, ADD CONSTRAINT "agent_task_project_id_fkey" FOREIGN KEY ("project_id") REFERENCES "project" ("id") ON UPDATE NO ACTION ON DELETE SET NULL, ADD CONSTRAINT "agent_task_session_id_fkey" FOREIGN KEY ("session_id") REFERENCES "ctx_conversation" ("session_id") ON UPDATE NO ACTION ON DELETE RESTRICT, ADD CONSTRAINT "agent_task_user_id_fkey" FOREIGN KEY ("user_id") REFERENCES "auth_user" ("id") ON UPDATE NO ACTION ON DELETE CASCADE;
-- Modify "agent_task_blocker" table
ALTER TABLE "agent_task_blocker" ADD CONSTRAINT "agent_task_blocker_created_by_run_id_fkey" FOREIGN KEY ("created_by_run_id") REFERENCES "agent_task_run" ("id") ON UPDATE NO ACTION ON DELETE SET NULL, ADD CONSTRAINT "agent_task_blocker_task_id_fkey" FOREIGN KEY ("task_id") REFERENCES "agent_task" ("id") ON UPDATE NO ACTION ON DELETE CASCADE;
-- Modify "agent_task_criterion" table
ALTER TABLE "agent_task_criterion" ADD CONSTRAINT "agent_task_criterion_task_id_fkey" FOREIGN KEY ("task_id") REFERENCES "agent_task" ("id") ON UPDATE NO ACTION ON DELETE CASCADE;
-- Modify "agent_task_dep" table
ALTER TABLE "agent_task_dep" ADD CONSTRAINT "agent_task_dep_dep_task_id_fkey" FOREIGN KEY ("dep_task_id") REFERENCES "agent_task" ("id") ON UPDATE NO ACTION ON DELETE CASCADE, ADD CONSTRAINT "agent_task_dep_task_id_fkey" FOREIGN KEY ("task_id") REFERENCES "agent_task" ("id") ON UPDATE NO ACTION ON DELETE CASCADE, ADD CONSTRAINT "agent_task_dep_waived_by_user_fkey" FOREIGN KEY ("waived_by_user") REFERENCES "auth_user" ("id") ON UPDATE NO ACTION ON DELETE SET NULL;
-- Modify "agent_task_dispatch_hint" table
ALTER TABLE "agent_task_dispatch_hint" ADD CONSTRAINT "agent_task_dispatch_hint_executor_agent_id_fkey" FOREIGN KEY ("executor_agent_id") REFERENCES "agent" ("id") ON UPDATE NO ACTION ON DELETE CASCADE, ADD CONSTRAINT "agent_task_dispatch_hint_goal_id_fkey" FOREIGN KEY ("goal_id") REFERENCES "agent_goal" ("id") ON UPDATE NO ACTION ON DELETE CASCADE, ADD CONSTRAINT "agent_task_dispatch_hint_task_id_fkey" FOREIGN KEY ("task_id") REFERENCES "agent_task" ("id") ON UPDATE NO ACTION ON DELETE CASCADE;
-- Modify "agent_task_event" table
ALTER TABLE "agent_task_event" ADD CONSTRAINT "agent_task_event_blocker_id_fkey" FOREIGN KEY ("blocker_id") REFERENCES "agent_task_blocker" ("id") ON UPDATE NO ACTION ON DELETE SET NULL, ADD CONSTRAINT "agent_task_event_goal_id_fkey" FOREIGN KEY ("goal_id") REFERENCES "agent_goal" ("id") ON UPDATE NO ACTION ON DELETE CASCADE, ADD CONSTRAINT "agent_task_event_review_id_fkey" FOREIGN KEY ("review_id") REFERENCES "agent_review" ("id") ON UPDATE NO ACTION ON DELETE SET NULL, ADD CONSTRAINT "agent_task_event_run_id_fkey" FOREIGN KEY ("run_id") REFERENCES "agent_task_run" ("id") ON UPDATE NO ACTION ON DELETE SET NULL, ADD CONSTRAINT "agent_task_event_task_id_fkey" FOREIGN KEY ("task_id") REFERENCES "agent_task" ("id") ON UPDATE NO ACTION ON DELETE CASCADE;
-- Modify "agent_task_run" table
ALTER TABLE "agent_task_run" ADD CONSTRAINT "agent_task_run_agent_id_fkey" FOREIGN KEY ("agent_id") REFERENCES "agent" ("id") ON UPDATE NO ACTION ON DELETE SET NULL, ADD CONSTRAINT "agent_task_run_executor_agent_id_fkey" FOREIGN KEY ("executor_agent_id") REFERENCES "agent" ("id") ON UPDATE NO ACTION ON DELETE SET NULL, ADD CONSTRAINT "agent_task_run_goal_id_fkey" FOREIGN KEY ("goal_id") REFERENCES "agent_goal" ("id") ON UPDATE NO ACTION ON DELETE CASCADE, ADD CONSTRAINT "agent_task_run_task_id_fkey" FOREIGN KEY ("task_id") REFERENCES "agent_task" ("id") ON UPDATE NO ACTION ON DELETE CASCADE, ADD CONSTRAINT "agent_task_run_user_id_fkey" FOREIGN KEY ("user_id") REFERENCES "auth_user" ("id") ON UPDATE NO ACTION ON DELETE CASCADE;
-- Modify "auth_credential" table
ALTER TABLE "auth_credential" ADD CONSTRAINT "auth_credential_user_id_fkey" FOREIGN KEY ("user_id") REFERENCES "auth_user" ("id") ON UPDATE NO ACTION ON DELETE CASCADE;
-- Modify "auth_identity" table
ALTER TABLE "auth_identity" ADD CONSTRAINT "auth_identity_user_id_fkey" FOREIGN KEY ("user_id") REFERENCES "auth_user" ("id") ON UPDATE NO ACTION ON DELETE CASCADE;
-- Modify "auth_session" table
ALTER TABLE "auth_session" ADD CONSTRAINT "auth_session_user_id_fkey" FOREIGN KEY ("user_id") REFERENCES "auth_user" ("id") ON UPDATE NO ACTION ON DELETE CASCADE;
-- Modify "auth_user" table
ALTER TABLE "auth_user" ADD CONSTRAINT "auth_user_default_agent_id_fkey" FOREIGN KEY ("default_agent_id") REFERENCES "agent" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION, ADD CONSTRAINT "auth_user_notify_identity_id_fkey" FOREIGN KEY ("notify_identity_id") REFERENCES "channel_identity" ("id") ON UPDATE NO ACTION ON DELETE SET NULL;
-- Modify "auth_user_agent" table
ALTER TABLE "auth_user_agent" ADD CONSTRAINT "auth_user_agent_agent_id_fkey" FOREIGN KEY ("agent_id") REFERENCES "agent" ("id") ON UPDATE NO ACTION ON DELETE CASCADE, ADD CONSTRAINT "auth_user_agent_user_id_fkey" FOREIGN KEY ("user_id") REFERENCES "auth_user" ("id") ON UPDATE NO ACTION ON DELETE CASCADE;
-- Modify "auth_user_token" table
ALTER TABLE "auth_user_token" ADD CONSTRAINT "auth_user_token_user_id_fkey" FOREIGN KEY ("user_id") REFERENCES "auth_user" ("id") ON UPDATE NO ACTION ON DELETE CASCADE;
-- Modify "channel" table
ALTER TABLE "channel" ADD CONSTRAINT "channel_agent_id_fkey" FOREIGN KEY ("agent_id") REFERENCES "agent" ("id") ON UPDATE NO ACTION ON DELETE SET NULL;
-- Modify "channel_agent" table
ALTER TABLE "channel_agent" ADD CONSTRAINT "channel_agent_agent_id_fkey" FOREIGN KEY ("agent_id") REFERENCES "agent" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION;
-- Modify "channel_group_member" table
ALTER TABLE "channel_group_member" ADD CONSTRAINT "channel_group_member_agent_id_fkey" FOREIGN KEY ("agent_id") REFERENCES "agent" ("id") ON UPDATE NO ACTION ON DELETE CASCADE, ADD CONSTRAINT "channel_group_member_group_id_fkey" FOREIGN KEY ("group_id") REFERENCES "ctx_group_state" ("id") ON UPDATE NO ACTION ON DELETE CASCADE, ADD CONSTRAINT "channel_group_member_reply_channel_id_fkey" FOREIGN KEY ("reply_channel_id") REFERENCES "channel" ("id") ON UPDATE NO ACTION ON DELETE CASCADE;
-- Modify "channel_identity" table
ALTER TABLE "channel_identity" ADD CONSTRAINT "channel_identity_user_id_fkey" FOREIGN KEY ("user_id") REFERENCES "auth_user" ("id") ON UPDATE NO ACTION ON DELETE CASCADE;
-- Modify "ctx_agent_memory" table
ALTER TABLE "ctx_agent_memory" ADD CONSTRAINT "ctx_agent_memory_agent_id_fkey" FOREIGN KEY ("agent_id") REFERENCES "agent" ("id") ON UPDATE NO ACTION ON DELETE CASCADE, ADD CONSTRAINT "ctx_agent_memory_user_id_fkey" FOREIGN KEY ("user_id") REFERENCES "auth_user" ("id") ON UPDATE NO ACTION ON DELETE CASCADE;
-- Modify "ctx_agent_memory_changelog" table
ALTER TABLE "ctx_agent_memory_changelog" ADD CONSTRAINT "ctx_agent_memory_changelog_agent_id_fkey" FOREIGN KEY ("agent_id") REFERENCES "agent" ("id") ON UPDATE NO ACTION ON DELETE CASCADE, ADD CONSTRAINT "ctx_agent_memory_changelog_user_id_fkey" FOREIGN KEY ("user_id") REFERENCES "auth_user" ("id") ON UPDATE NO ACTION ON DELETE CASCADE;
-- Modify "ctx_agent_memory_snapshot" table
ALTER TABLE "ctx_agent_memory_snapshot" ADD CONSTRAINT "ctx_agent_memory_snapshot_agent_id_fkey" FOREIGN KEY ("agent_id") REFERENCES "agent" ("id") ON UPDATE NO ACTION ON DELETE CASCADE, ADD CONSTRAINT "ctx_agent_memory_snapshot_user_id_fkey" FOREIGN KEY ("user_id") REFERENCES "auth_user" ("id") ON UPDATE NO ACTION ON DELETE CASCADE;
-- Modify "ctx_group_dispatch" table
ALTER TABLE "ctx_group_dispatch" ADD CONSTRAINT "ctx_group_dispatch_agent_id_fkey" FOREIGN KEY ("agent_id") REFERENCES "agent" ("id") ON UPDATE NO ACTION ON DELETE CASCADE, ADD CONSTRAINT "ctx_group_dispatch_group_id_fkey" FOREIGN KEY ("group_id") REFERENCES "ctx_group_state" ("id") ON UPDATE NO ACTION ON DELETE CASCADE, ADD CONSTRAINT "ctx_group_dispatch_group_message_id_fkey" FOREIGN KEY ("group_message_id") REFERENCES "ctx_group_message" ("id") ON UPDATE NO ACTION ON DELETE CASCADE, ADD CONSTRAINT "ctx_group_dispatch_reply_channel_id_fkey" FOREIGN KEY ("reply_channel_id") REFERENCES "channel" ("id") ON UPDATE NO ACTION ON DELETE CASCADE;
-- Modify "ctx_group_ingest_cursor" table
ALTER TABLE "ctx_group_ingest_cursor" ADD CONSTRAINT "ctx_group_ingest_cursor_group_id_fkey" FOREIGN KEY ("group_id") REFERENCES "ctx_group_state" ("id") ON UPDATE NO ACTION ON DELETE CASCADE;
-- Modify "ctx_group_ingest_error" table
ALTER TABLE "ctx_group_ingest_error" ADD CONSTRAINT "ctx_group_ingest_error_group_id_fkey" FOREIGN KEY ("group_id") REFERENCES "ctx_group_state" ("id") ON UPDATE NO ACTION ON DELETE CASCADE;
-- Modify "ctx_group_memory" table
ALTER TABLE "ctx_group_memory" ADD CONSTRAINT "ctx_group_memory_group_id_fkey" FOREIGN KEY ("group_id") REFERENCES "ctx_group_state" ("id") ON UPDATE NO ACTION ON DELETE CASCADE;
-- Modify "ctx_group_message" table
ALTER TABLE "ctx_group_message" ADD CONSTRAINT "ctx_group_message_group_id_fkey" FOREIGN KEY ("group_id") REFERENCES "ctx_group_state" ("id") ON UPDATE NO ACTION ON DELETE CASCADE;
-- Modify "ctx_group_outbox" table
ALTER TABLE "ctx_group_outbox" ADD CONSTRAINT "ctx_group_outbox_group_id_fkey" FOREIGN KEY ("group_id") REFERENCES "ctx_group_state" ("id") ON UPDATE NO ACTION ON DELETE CASCADE, ADD CONSTRAINT "ctx_group_outbox_group_message_id_fkey" FOREIGN KEY ("group_message_id") REFERENCES "ctx_group_message" ("id") ON UPDATE NO ACTION ON DELETE CASCADE;
-- Modify "ctx_group_state" table
ALTER TABLE "ctx_group_state" ADD CONSTRAINT "ctx_group_state_created_by_user_id_fkey" FOREIGN KEY ("created_by_user_id") REFERENCES "auth_user" ("id") ON UPDATE NO ACTION ON DELETE CASCADE;
-- Modify "ctx_item" table
ALTER TABLE "ctx_item" ADD CONSTRAINT "ctx_item_conversation_id_fkey" FOREIGN KEY ("conversation_id") REFERENCES "ctx_conversation" ("id") ON UPDATE NO ACTION ON DELETE CASCADE, ADD CONSTRAINT "ctx_item_message_id_fkey" FOREIGN KEY ("message_id") REFERENCES "ctx_message" ("id") ON UPDATE NO ACTION ON DELETE RESTRICT, ADD CONSTRAINT "ctx_item_summary_id_fkey" FOREIGN KEY ("summary_id") REFERENCES "ctx_summary" ("id") ON UPDATE NO ACTION ON DELETE RESTRICT;
-- Modify "ctx_message" table
ALTER TABLE "ctx_message" ADD CONSTRAINT "ctx_message_conversation_id_fkey" FOREIGN KEY ("conversation_id") REFERENCES "ctx_conversation" ("id") ON UPDATE NO ACTION ON DELETE CASCADE;
-- Modify "ctx_message_part" table
ALTER TABLE "ctx_message_part" ADD CONSTRAINT "ctx_message_part_message_id_fkey" FOREIGN KEY ("message_id") REFERENCES "ctx_message" ("id") ON UPDATE NO ACTION ON DELETE CASCADE;
-- Modify "ctx_summary" table
ALTER TABLE "ctx_summary" ADD CONSTRAINT "ctx_summary_conversation_id_fkey" FOREIGN KEY ("conversation_id") REFERENCES "ctx_conversation" ("id") ON UPDATE NO ACTION ON DELETE CASCADE;
-- Modify "ctx_summary_message" table
ALTER TABLE "ctx_summary_message" ADD CONSTRAINT "ctx_summary_message_message_id_fkey" FOREIGN KEY ("message_id") REFERENCES "ctx_message" ("id") ON UPDATE NO ACTION ON DELETE RESTRICT, ADD CONSTRAINT "ctx_summary_message_summary_id_fkey" FOREIGN KEY ("summary_id") REFERENCES "ctx_summary" ("id") ON UPDATE NO ACTION ON DELETE CASCADE;
-- Modify "ctx_summary_parent" table
ALTER TABLE "ctx_summary_parent" ADD CONSTRAINT "ctx_summary_parent_parent_summary_id_fkey" FOREIGN KEY ("parent_summary_id") REFERENCES "ctx_summary" ("id") ON UPDATE NO ACTION ON DELETE RESTRICT, ADD CONSTRAINT "ctx_summary_parent_summary_id_fkey" FOREIGN KEY ("summary_id") REFERENCES "ctx_summary" ("id") ON UPDATE NO ACTION ON DELETE CASCADE;
-- Modify "project" table
ALTER TABLE "project" ADD CONSTRAINT "project_agent_id_fkey" FOREIGN KEY ("agent_id") REFERENCES "agent" ("id") ON UPDATE NO ACTION ON DELETE CASCADE, ADD CONSTRAINT "project_user_id_fkey" FOREIGN KEY ("user_id") REFERENCES "auth_user" ("id") ON UPDATE NO ACTION ON DELETE CASCADE;
-- Modify "recally_article" table
ALTER TABLE "recally_article" ADD CONSTRAINT "recally_article_agent_id_fkey" FOREIGN KEY ("agent_id") REFERENCES "agent" ("id") ON UPDATE NO ACTION ON DELETE SET NULL, ADD CONSTRAINT "recally_article_user_id_fkey" FOREIGN KEY ("user_id") REFERENCES "auth_user" ("id") ON UPDATE NO ACTION ON DELETE CASCADE;
-- Modify "recally_digest" table
ALTER TABLE "recally_digest" ADD CONSTRAINT "recally_digest_user_id_fkey" FOREIGN KEY ("user_id") REFERENCES "auth_user" ("id") ON UPDATE NO ACTION ON DELETE CASCADE;
-- Modify "recally_digest_article" table
ALTER TABLE "recally_digest_article" ADD CONSTRAINT "recally_digest_article_article_id_fkey" FOREIGN KEY ("article_id") REFERENCES "recally_article" ("id") ON UPDATE NO ACTION ON DELETE CASCADE, ADD CONSTRAINT "recally_digest_article_digest_id_fkey" FOREIGN KEY ("digest_id") REFERENCES "recally_digest" ("id") ON UPDATE NO ACTION ON DELETE CASCADE;
-- Modify "recally_feed" table
ALTER TABLE "recally_feed" ADD CONSTRAINT "recally_feed_agent_id_fkey" FOREIGN KEY ("agent_id") REFERENCES "agent" ("id") ON UPDATE NO ACTION ON DELETE SET NULL, ADD CONSTRAINT "recally_feed_user_id_fkey" FOREIGN KEY ("user_id") REFERENCES "auth_user" ("id") ON UPDATE NO ACTION ON DELETE CASCADE;
-- Modify "recally_feed_entry" table
ALTER TABLE "recally_feed_entry" ADD CONSTRAINT "recally_feed_entry_article_id_fkey" FOREIGN KEY ("article_id") REFERENCES "recally_article" ("id") ON UPDATE NO ACTION ON DELETE SET NULL, ADD CONSTRAINT "recally_feed_entry_feed_id_fkey" FOREIGN KEY ("feed_id") REFERENCES "recally_feed" ("id") ON UPDATE NO ACTION ON DELETE CASCADE;
-- Modify "sched_job_run" table
ALTER TABLE "sched_job_run" ADD CONSTRAINT "sched_job_run_job_id_fkey" FOREIGN KEY ("job_id") REFERENCES "sched_job" ("id") ON UPDATE NO ACTION ON DELETE CASCADE;
-- Modify "share" table
ALTER TABLE "share" ADD CONSTRAINT "share_user_id_fkey" FOREIGN KEY ("user_id") REFERENCES "auth_user" ("id") ON UPDATE NO ACTION ON DELETE CASCADE;
-- Modify "skill" table
ALTER TABLE "skill" ADD CONSTRAINT "skill_agent_id_fkey" FOREIGN KEY ("agent_id") REFERENCES "agent" ("id") ON UPDATE NO ACTION ON DELETE CASCADE, ADD CONSTRAINT "skill_user_id_fkey" FOREIGN KEY ("user_id") REFERENCES "auth_user" ("id") ON UPDATE NO ACTION ON DELETE CASCADE;
-- Modify "skill_file" table
ALTER TABLE "skill_file" ADD CONSTRAINT "skill_file_skill_id_fkey" FOREIGN KEY ("skill_id") REFERENCES "skill" ("id") ON UPDATE NO ACTION ON DELETE CASCADE;
-- Modify "vault_entry" table
ALTER TABLE "vault_entry" ADD CONSTRAINT "vault_entry_agent_id_fkey" FOREIGN KEY ("agent_id") REFERENCES "agent" ("id") ON UPDATE NO ACTION ON DELETE CASCADE, ADD CONSTRAINT "vault_entry_user_id_fkey" FOREIGN KEY ("user_id") REFERENCES "auth_user" ("id") ON UPDATE NO ACTION ON DELETE CASCADE;
