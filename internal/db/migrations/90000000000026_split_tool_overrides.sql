-- +goose Up
-- The seven union tools became one tool per action, and four recally tools were
-- renamed to <resource>_<action>. A tool_override row is keyed by name, so every
-- row written against an old name would stop matching and silently re-enable a
-- capability the user or the operator switched off.
--
-- This is the expand half of expand-then-contract (rules/agent-tools.md §10):
-- fan each old name out to its new names, merge deny-wins, and keep the old rows
-- so a rollback still has something to fold back into.
--
-- The mapping is a CTE rather than a temp table on purpose: sqlc reads these
-- migrations to build the schema, and a temp table would become a generated
-- model.
--
-- bool_and folds before the insert as well as inside it: two old names can map
-- to the same new name (the recally union and the recally rename both reach
-- recally_article_save), and ON CONFLICT DO UPDATE cannot touch one row twice
-- in a single command.
WITH split_tool_map (old_name, new_name, introduced) AS (VALUES
    ('goal', 'goal_cancel', TRUE),
    ('goal', 'goal_create', TRUE),
    ('goal', 'goal_get', TRUE),
    ('goal', 'goal_list', TRUE),
    ('scheduler', 'scheduler_job_create', TRUE),
    ('scheduler', 'scheduler_job_delete', TRUE),
    ('scheduler', 'scheduler_job_get', TRUE),
    ('scheduler', 'scheduler_job_list', TRUE),
    ('scheduler', 'scheduler_job_pause', TRUE),
    ('scheduler', 'scheduler_job_resume', TRUE),
    ('scheduler', 'scheduler_job_update', TRUE),
    ('workflow', 'workflow_get', TRUE),
    ('workflow', 'workflow_list', TRUE),
    ('workflow', 'workflow_run', TRUE),
    ('workflow', 'workflow_save', TRUE),
    ('oauth', 'oauth_connect', TRUE),
    ('oauth', 'oauth_disconnect', TRUE),
    ('oauth', 'oauth_flow_status', TRUE),
    ('oauth', 'oauth_list', TRUE),
    ('email', 'email_account_list', TRUE),
    ('email', 'email_message_list', TRUE),
    ('email', 'email_message_read', TRUE),
    ('email', 'email_message_send', TRUE),
    ('share', 'share_create_article', TRUE),
    ('share', 'share_create_artifact', TRUE),
    ('share', 'share_list', TRUE),
    ('share', 'share_revoke', TRUE),
    ('vault', 'vault_secret_delete', TRUE),
    ('vault', 'vault_secret_list', TRUE),
    ('vault', 'vault_secret_set', TRUE),
    -- Exact renames inside the already-split recally family.
    ('recally_get_article', 'recally_article_get', TRUE),
    ('recally_list_articles', 'recally_article_list', TRUE),
    ('recally_save_article', 'recally_article_save', TRUE),
    ('recally_digest', 'recally_digest_get', TRUE),
    -- The recally union predates #1171, which split it without a migration.
    -- Rows written against it are still out there, so fan them out too. The
    -- eight names marked FALSE existed before this migration, which is what
    -- keeps `down` from deleting them.
    ('recally', 'recally_article_get', TRUE),
    ('recally', 'recally_article_list', TRUE),
    ('recally', 'recally_article_save', TRUE),
    ('recally', 'recally_digest_get', TRUE),
    ('recally', 'recally_digest_save', FALSE),
    ('recally', 'recally_entry_add', FALSE),
    ('recally', 'recally_entry_list', FALSE),
    ('recally', 'recally_entry_update', FALSE),
    ('recally', 'recally_feed_add', FALSE),
    ('recally', 'recally_feed_list', FALSE),
    ('recally', 'recally_feed_poll', FALSE),
    ('recally', 'recally_feed_remove', FALSE)
)
INSERT INTO tool_override (tool_name, scope, user_id, agent_id, enabled)
SELECT map.new_name, override.scope, override.user_id, override.agent_id, bool_and(override.enabled)
FROM tool_override AS override
JOIN split_tool_map AS map ON map.old_name = override.tool_name
GROUP BY map.new_name, override.scope, override.user_id, override.agent_id
ON CONFLICT (tool_name, scope, user_id, agent_id) DO UPDATE
SET enabled = tool_override.enabled AND EXCLUDED.enabled, updated_at = now();

-- +goose Down
-- One statement so the fold and the cleanup read the same snapshot: the fold
-- must see the action rows the delete is about to remove. They touch disjoint
-- names, so neither can undo the other.
--
-- The fold ANDs the action rows back onto the old name with bool_and per scope:
-- a capability disabled on any action stays disabled on the union, which would
-- otherwise hand it back.
--
-- The pre-#1171 "recally" union is excluded from the fold: `up` only honoured
-- rows that already existed and kept them, so folding into it would mint a row
-- this migration never created, and a later re-run of `up` would fan that row's
-- deny out across the eight recally tools nobody had disabled.
WITH split_tool_map (old_name, new_name, introduced) AS (VALUES
    ('goal', 'goal_cancel', TRUE),
    ('goal', 'goal_create', TRUE),
    ('goal', 'goal_get', TRUE),
    ('goal', 'goal_list', TRUE),
    ('scheduler', 'scheduler_job_create', TRUE),
    ('scheduler', 'scheduler_job_delete', TRUE),
    ('scheduler', 'scheduler_job_get', TRUE),
    ('scheduler', 'scheduler_job_list', TRUE),
    ('scheduler', 'scheduler_job_pause', TRUE),
    ('scheduler', 'scheduler_job_resume', TRUE),
    ('scheduler', 'scheduler_job_update', TRUE),
    ('workflow', 'workflow_get', TRUE),
    ('workflow', 'workflow_list', TRUE),
    ('workflow', 'workflow_run', TRUE),
    ('workflow', 'workflow_save', TRUE),
    ('oauth', 'oauth_connect', TRUE),
    ('oauth', 'oauth_disconnect', TRUE),
    ('oauth', 'oauth_flow_status', TRUE),
    ('oauth', 'oauth_list', TRUE),
    ('email', 'email_account_list', TRUE),
    ('email', 'email_message_list', TRUE),
    ('email', 'email_message_read', TRUE),
    ('email', 'email_message_send', TRUE),
    ('share', 'share_create_article', TRUE),
    ('share', 'share_create_artifact', TRUE),
    ('share', 'share_list', TRUE),
    ('share', 'share_revoke', TRUE),
    ('vault', 'vault_secret_delete', TRUE),
    ('vault', 'vault_secret_list', TRUE),
    ('vault', 'vault_secret_set', TRUE),
    -- Exact renames inside the already-split recally family.
    ('recally_get_article', 'recally_article_get', TRUE),
    ('recally_list_articles', 'recally_article_list', TRUE),
    ('recally_save_article', 'recally_article_save', TRUE),
    ('recally_digest', 'recally_digest_get', TRUE),
    -- The recally union predates #1171, which split it without a migration.
    -- Rows written against it are still out there, so fan them out too. The
    -- eight names marked FALSE existed before this migration, which is what
    -- keeps `down` from deleting them.
    ('recally', 'recally_article_get', TRUE),
    ('recally', 'recally_article_list', TRUE),
    ('recally', 'recally_article_save', TRUE),
    ('recally', 'recally_digest_get', TRUE),
    ('recally', 'recally_digest_save', FALSE),
    ('recally', 'recally_entry_add', FALSE),
    ('recally', 'recally_entry_list', FALSE),
    ('recally', 'recally_entry_update', FALSE),
    ('recally', 'recally_feed_add', FALSE),
    ('recally', 'recally_feed_list', FALSE),
    ('recally', 'recally_feed_poll', FALSE),
    ('recally', 'recally_feed_remove', FALSE)
)
, folded AS (
    INSERT INTO tool_override (tool_name, scope, user_id, agent_id, enabled)
    SELECT map.old_name, override.scope, override.user_id, override.agent_id, bool_and(override.enabled)
    FROM tool_override AS override
    JOIN split_tool_map AS map ON map.new_name = override.tool_name
    WHERE map.old_name <> 'recally'
    GROUP BY map.old_name, override.scope, override.user_id, override.agent_id
    ON CONFLICT (tool_name, scope, user_id, agent_id) DO UPDATE
    SET enabled = tool_override.enabled AND EXCLUDED.enabled, updated_at = now()
    RETURNING 1
)
-- Only the names this migration introduced go away; the eight recally names
-- that predate it stay, because the schema they belong to predates it too.
DELETE FROM tool_override
WHERE tool_name IN (SELECT new_name FROM split_tool_map WHERE introduced);
