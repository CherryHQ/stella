-- +goose Up
-- The `memory` union became two action tools. A tool_override row is keyed by
-- name, so a row written against the old name would stop matching and silently
-- re-enable recall for a user or an operator who had switched it off.
--
-- Same expand half as 90000000000026 and 90000000000027
-- (rules/agent-tools.md §10): fan the old name out to both new names, merge
-- deny-wins, and keep the old row so a rollback still has something to fold
-- back into. The mapping is a CTE because sqlc reads these migrations and a
-- temp table would become a generated model.
WITH split_tool_map (old_name, new_name) AS (VALUES
    ('memory', 'memory_read'),
    ('memory', 'memory_search')
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
-- The fold ANDs the action rows back onto the union name with bool_and per
-- scope: recall disabled on either tool stays disabled on the union, which
-- would otherwise hand it back.
WITH split_tool_map (old_name, new_name) AS (VALUES
    ('memory', 'memory_read'),
    ('memory', 'memory_search')
)
, folded AS (
    INSERT INTO tool_override (tool_name, scope, user_id, agent_id, enabled)
    SELECT map.old_name, override.scope, override.user_id, override.agent_id, bool_and(override.enabled)
    FROM tool_override AS override
    JOIN split_tool_map AS map ON map.new_name = override.tool_name
    GROUP BY map.old_name, override.scope, override.user_id, override.agent_id
    ON CONFLICT (tool_name, scope, user_id, agent_id) DO UPDATE
    SET enabled = tool_override.enabled AND EXCLUDED.enabled, updated_at = now()
    RETURNING 1
)
DELETE FROM tool_override
WHERE tool_name IN (SELECT new_name FROM split_tool_map);
