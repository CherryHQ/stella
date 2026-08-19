-- name: CreateAgentLLMCall :one
INSERT INTO agent_llm_call (
    id, session_id, agent_id, provider, model, usage_reported,
    input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, cost_usd,
    duration_ms, time_to_first_token_ms, stop_reason, error, occurred_at
)
VALUES (
    $1, $2, $3, $4, $5, $6,
    $7, $8, $9, $10, $11,
    $12, $13, $14, $15, $16
)
RETURNING *;

-- name: ListAgentLLMCallUsageBySessionID :many
-- A total is NULL unless every call in the model bucket reported usage (and,
-- for cost, every reported call had a configured price). Returning a partial
-- total would make an unavailable provider report look like free usage.
SELECT
    provider,
    model,
    COUNT(*)::bigint AS call_count,
    COUNT(*) FILTER (WHERE usage_reported)::bigint AS reported_call_count,
    COUNT(*) FILTER (WHERE cost_usd IS NOT NULL)::bigint AS priced_call_count,
    COALESCE(SUM(input_tokens), 0)::bigint AS input_tokens,
    COALESCE(SUM(output_tokens), 0)::bigint AS output_tokens,
    COALESCE(SUM(cache_read_tokens), 0)::bigint AS cache_read_tokens,
    COALESCE(SUM(cache_write_tokens), 0)::bigint AS cache_write_tokens,
    CASE WHEN BOOL_AND(usage_reported) AND BOOL_AND(cost_usd IS NOT NULL) THEN SUM(cost_usd) ELSE NULL::numeric END AS cost_usd
FROM agent_llm_call
WHERE session_id = $1
GROUP BY provider, model
ORDER BY provider, model;
