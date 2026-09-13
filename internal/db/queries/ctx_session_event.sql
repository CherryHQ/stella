-- name: NextSessionEventSeq :one
-- Contiguous per-session sequence from the session row's monotonic counter —
-- pruning ctx_session_event can never rewind it. Allocated under the
-- conversation write lock the caller already holds.
UPDATE ctx_conversation SET event_seq = event_seq + 1
WHERE session_id = $1
RETURNING event_seq;

-- name: InsertSessionEvent :exec
INSERT INTO ctx_session_event (session_id, run_id, execution_id, seq, event)
VALUES ($1, $2, $3, $4, $5);

-- name: ReadSessionEventsSince :many
-- Cursor replay: bounded page of events after a sequence.
SELECT * FROM ctx_session_event
WHERE session_id = $1 AND seq > $2
ORDER BY seq
LIMIT $3;

-- name: LatestSessionEventSeq :one
SELECT COALESCE(MAX(seq), 0)::bigint FROM ctx_session_event WHERE session_id = $1;

-- name: PruneSessionEvents :execrows
-- Retention: legacy rows without an execution coordinate age out on their
-- own created_at. Execution-scoped rows are prunable only as a whole segment
-- once the execution's turn_terminal marker itself is past the window — the
-- terminal's age, not each event's, is the anchor, so a turn whose start is
-- old but whose terminal just landed keeps its full replay window while a
-- bootstrapping observer rebuilds it. One statement snapshot decides which
-- segments go.
DELETE FROM ctx_session_event e
WHERE (e.execution_id IS NULL
       AND e.created_at < now() - interval '24 hours')
   OR (e.execution_id IS NOT NULL AND EXISTS (
       SELECT 1 FROM ctx_session_event t
       WHERE t.session_id = e.session_id
         AND t.execution_id = e.execution_id
         AND t.event->>'type' = 'turn_terminal'
         AND t.created_at < now() - interval '24 hours'));

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

-- name: ReadSessionEventsForExecution :many
-- Durable replay of one execution's turn (non-run paths): events stamped with
-- the lease token, after cursor.
SELECT * FROM ctx_session_event
WHERE session_id = $1 AND execution_id = $2 AND seq > $3
ORDER BY seq
LIMIT $4;

-- name: GetSessionStartEvent :one
-- The execution's turn_start marker carries the canonical history boundary
-- (history_before_seq) a cold reload rebuilds from.
SELECT * FROM ctx_session_event
WHERE session_id = $1 AND execution_id = $2 AND event->>'type' = 'turn_start'
ORDER BY seq DESC
LIMIT 1;

-- name: GetSessionStartEventForRun :one
-- Same start marker resolved by run id — run watchers hold the run coordinate,
-- not the execution token.
SELECT * FROM ctx_session_event
WHERE session_id = $1 AND run_id = $2 AND event->>'type' = 'turn_start'
ORDER BY seq DESC
LIMIT 1;

-- name: GetSessionTerminalEvent :one
-- The explicit turn_terminal marker for one execution, written inside the
-- finish/reap transaction. Presence — not lease disappearance — ends a tail.
SELECT * FROM ctx_session_event
WHERE session_id = $1 AND execution_id = $2 AND event->>'type' = 'turn_terminal'
ORDER BY seq DESC
LIMIT 1;
