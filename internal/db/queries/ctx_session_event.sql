-- name: NextSessionEventSeq :one
-- Contiguous per-session sequence, allocated under the session advisory lock
-- the caller already holds.
SELECT COALESCE(MAX(seq), 0) + 1::bigint FROM ctx_session_event WHERE session_id = $1;

-- name: InsertSessionEvent :exec
INSERT INTO ctx_session_event (session_id, run_id, seq, event)
VALUES ($1, $2, $3, $4);

-- name: ReadSessionEventsSince :many
-- Cursor replay: bounded page of events after a sequence.
SELECT * FROM ctx_session_event
WHERE session_id = $1 AND seq > $2
ORDER BY seq
LIMIT $3;

-- name: LatestSessionEventSeq :one
SELECT COALESCE(MAX(seq), 0)::bigint FROM ctx_session_event WHERE session_id = $1;

-- name: PruneSessionEvents :execrows
-- Retention: drop events older than the keep window.
DELETE FROM ctx_session_event
WHERE created_at < clock_timestamp() - interval '24 hours';

-- name: ReadSessionEventsForRun :many
-- Durable replay of one run's turn: events linked to the run, after cursor.
SELECT * FROM ctx_session_event
WHERE session_id = $1 AND run_id = $2 AND seq > $3
ORDER BY seq
LIMIT $4;

-- name: MinSessionEventSeqForRun :one
-- Earliest surviving seq for a run: a reconnecting watcher compares its
-- cursor against this to detect truncation and rebuild from transcript.
SELECT COALESCE(MIN(seq), 0)::bigint FROM ctx_session_event
WHERE session_id = $1 AND run_id = $2;
