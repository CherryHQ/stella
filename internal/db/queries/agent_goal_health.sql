-- name: GetGoalHealthReport :one
WITH scoped_goal AS (
    SELECT g.*
    FROM agent_goal g
    WHERE g.created_at >= sqlc.arg(since_at)
      AND (sqlc.narg(user_id)::uuid IS NULL OR g.user_id = sqlc.narg(user_id)::uuid)
      AND (sqlc.narg(agent_id)::text IS NULL OR g.agent_id = sqlc.narg(agent_id)::text)
      AND g.id = ANY(sqlc.arg(goal_ids)::text[])
),
scoped_attempt AS (
    SELECT a.*
    FROM agent_goal_attempt a
    JOIN scoped_goal g ON g.id = a.goal_id
),
scoped_event AS (
    SELECT e.*
    FROM agent_goal_acceptance_event e
    JOIN scoped_goal g ON g.id = e.goal_id
),
purpose_retry AS (
    SELECT purpose, goal_id, GREATEST(MAX(attempt_no) - 1, 0)::bigint AS retries
    FROM scoped_attempt
    GROUP BY purpose, goal_id
),
purpose_attempt AS (
    SELECT
        p.purpose,
        COALESCE(a.total, 0)::bigint AS total,
        COALESCE(a.succeeded, 0)::bigint AS succeeded,
        CASE WHEN COALESCE(a.total, 0) = 0 THEN 0 ELSE COALESCE(a.succeeded, 0)::double precision / a.total END AS success_rate,
        COALESCE(r.avg_retries, 0)::double precision AS average_retries,
        COALESCE(r.max_retries, 0)::bigint AS max_retries
    FROM (
        SELECT 'decomposition'::text AS purpose
        UNION ALL SELECT 'execution'::text
        UNION ALL SELECT 'review'::text
    ) p
    LEFT JOIN (
        SELECT purpose, COUNT(*) AS total, COUNT(*) FILTER (WHERE status = 'submitted') AS succeeded
        FROM scoped_attempt
        GROUP BY purpose
    ) a ON a.purpose = p.purpose
    LEFT JOIN (
        SELECT purpose, AVG(retries)::double precision AS avg_retries, MAX(retries) AS max_retries
        FROM purpose_retry
        GROUP BY purpose
    ) r ON r.purpose = p.purpose
),
first_decomposition AS (
    SELECT
        COUNT(*)::bigint AS total,
        COUNT(*) FILTER (WHERE status = 'submitted')::bigint AS succeeded
    FROM scoped_attempt
    WHERE purpose = 'decomposition' AND attempt_no = 1
),
decomposition_counts AS (
    SELECT g.id, GREATEST(COUNT(a.id) - 1, 0)::bigint AS redecompositions
    FROM scoped_goal g
    LEFT JOIN scoped_attempt a ON a.goal_id = g.id AND a.purpose = 'decomposition'
    GROUP BY g.id
),
execution_failure_attempt AS (
    SELECT *
    FROM scoped_attempt
    WHERE purpose = 'execution'
      AND status IN ('failed', 'interrupted')
      AND failure_class != ''
),
model_budget_attempt AS (
    SELECT *
    FROM execution_failure_attempt
    WHERE failure_class = 'model'
),
flaky_dominant_blocked_goal AS (
    SELECT g.id
    FROM scoped_goal g
    JOIN execution_failure_attempt a ON a.goal_id = g.id
    WHERE g.lifecycle = 'blocked'
      AND g.block_reason = 'env_unavailable'
    GROUP BY g.id
    HAVING COUNT(*) FILTER (WHERE a.failure_class = 'flaky') > 0
       AND COUNT(*) FILTER (WHERE a.failure_class = 'flaky') * 2 >= COUNT(*)
),
attempt_latency AS (
    SELECT
        p.purpose,
        percentile_cont(0.5) WITHIN GROUP (ORDER BY EXTRACT(EPOCH FROM (a.finished_at - a.started_at)) * 1000)::double precision AS p50_ms,
        percentile_cont(0.95) WITHIN GROUP (ORDER BY EXTRACT(EPOCH FROM (a.finished_at - a.started_at)) * 1000)::double precision AS p95_ms
    FROM (
        SELECT 'decomposition'::text AS purpose
        UNION ALL SELECT 'execution'::text
        UNION ALL SELECT 'review'::text
    ) p
    LEFT JOIN scoped_attempt a ON a.purpose = p.purpose AND a.started_at IS NOT NULL AND a.finished_at IS NOT NULL
    GROUP BY p.purpose
),
goal_latency AS (
    SELECT
        percentile_cont(0.5) WITHIN GROUP (ORDER BY EXTRACT(EPOCH FROM (ended_at - created_at)) * 1000)::double precision AS p50_ms,
        percentile_cont(0.95) WITHIN GROUP (ORDER BY EXTRACT(EPOCH FROM (ended_at - created_at)) * 1000)::double precision AS p95_ms
    FROM (
        SELECT created_at, COALESCE(accepted_at, cancelled_at, updated_at) AS ended_at
        FROM scoped_goal
        WHERE lifecycle IN ('done', 'blocked')
    ) g
)
SELECT jsonb_build_object(
    'total_goals', (SELECT COUNT(*)::bigint FROM scoped_goal),
    'lifecycle_counts', COALESCE((
        SELECT jsonb_agg(jsonb_build_object('key', lifecycle, 'count', count) ORDER BY count DESC, lifecycle ASC)
        FROM (
            SELECT lifecycle, COUNT(*)::bigint AS count
            FROM scoped_goal
            GROUP BY lifecycle
        ) x
    ), '[]'::jsonb),
    'blocked_reason_counts', COALESCE((
        SELECT jsonb_agg(jsonb_build_object('key', block_reason, 'count', count) ORDER BY count DESC, block_reason ASC)
        FROM (
            SELECT block_reason, COUNT(*)::bigint AS count
            FROM scoped_goal
            WHERE lifecycle = 'blocked'
            GROUP BY block_reason
        ) x
    ), '[]'::jsonb),
    'attempt_purposes', COALESCE((
        SELECT jsonb_agg(jsonb_build_object(
            'purpose', purpose,
            'total', total,
            'succeeded', succeeded,
            'success_rate', success_rate,
            'average_retries', average_retries,
            'max_retries', max_retries
        ) ORDER BY purpose ASC)
        FROM purpose_attempt
    ), '[]'::jsonb),
    'failure_class_counts', COALESCE((
        SELECT jsonb_agg(jsonb_build_object('key', failure_class, 'count', count) ORDER BY count DESC, failure_class ASC)
        FROM (
            SELECT failure_class, COUNT(*)::bigint AS count
            FROM scoped_attempt
            WHERE failure_class != ''
            GROUP BY failure_class
        ) x
    ), '[]'::jsonb),
    'acceptance_events', jsonb_build_object(
        'total', (SELECT COUNT(*)::bigint FROM scoped_event),
        'passed', (SELECT COUNT(*)::bigint FROM scoped_event WHERE result = 'pass'),
        'failed', (SELECT COUNT(*)::bigint FROM scoped_event WHERE result = 'fail'),
        'pass_rate', CASE
            WHEN (SELECT COUNT(*) FROM scoped_event WHERE result IN ('pass', 'fail')) = 0 THEN 0
            ELSE (SELECT COUNT(*) FROM scoped_event WHERE result = 'pass')::double precision /
                 (SELECT COUNT(*) FROM scoped_event WHERE result IN ('pass', 'fail'))
        END
    ),
    'decomposition_quality', jsonb_build_object(
        'first_round_total', (SELECT total FROM first_decomposition),
        'first_round_succeeded', (SELECT succeeded FROM first_decomposition),
        'first_round_success_rate', CASE
            WHEN (SELECT total FROM first_decomposition) = 0 THEN 0
            ELSE (SELECT succeeded FROM first_decomposition)::double precision / (SELECT total FROM first_decomposition)
        END,
        'average_repair_rounds', COALESCE((
            SELECT AVG(repair_rounds)::double precision
            FROM scoped_attempt
            WHERE purpose = 'decomposition'
        ), 0),
        'redecomposition_counts', COALESCE((
            SELECT jsonb_agg(jsonb_build_object('key', redecompositions::text, 'count', count) ORDER BY redecompositions ASC)
            FROM (
                SELECT redecompositions, COUNT(*)::bigint AS count
                FROM decomposition_counts
                GROUP BY redecompositions
            ) x
        ), '[]'::jsonb)
    ),
    'budget_attribution', jsonb_build_object(
        'model_budget_attempts', (SELECT COUNT(*)::bigint FROM model_budget_attempt),
        'class_counts', COALESCE((
            SELECT jsonb_agg(jsonb_build_object(
                'key', failure_class,
                'count', count,
                'ratio', CASE WHEN total = 0 THEN 0 ELSE count::double precision / total END
            ) ORDER BY count DESC, failure_class ASC)
            FROM (
                SELECT failure_class, COUNT(*)::bigint AS count, SUM(COUNT(*)) OVER ()::bigint AS total
                FROM execution_failure_attempt
                GROUP BY failure_class
            ) x
        ), '[]'::jsonb),
        'flaky_dominant_blocked_goals', (SELECT COUNT(*)::bigint FROM flaky_dominant_blocked_goal)
    ),
    'latency', jsonb_build_object(
        'attempts', COALESCE((
            SELECT jsonb_agg(jsonb_build_object(
                'purpose', purpose,
                'p50_ms', p50_ms,
                'p95_ms', p95_ms
            ) ORDER BY purpose ASC)
            FROM attempt_latency
        ), '[]'::jsonb),
        'goal_e2e', jsonb_build_object(
            'p50_ms', (SELECT p50_ms FROM goal_latency),
            'p95_ms', (SELECT p95_ms FROM goal_latency)
        )
    )
)::jsonb AS report;
