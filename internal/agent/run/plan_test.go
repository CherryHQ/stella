package run

import (
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/db/dbtest"
)

// The claim/route/send scans are the hot paths every replica polls. With
// enable_seqscan off the planner must be able to serve each through its
// dedicated index; a seq scan here means the index is missing or unusable.
func TestHotScanPlansUseIndexes(t *testing.T) {
	db := dbtest.New(t)
	ctx := t.Context()

	conn, err := db.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "SET enable_seqscan = off"); err != nil {
		t.Fatal(err)
	}

	queries := map[string]string{
		"channel_inbox pending scan": `EXPLAIN SELECT * FROM channel_inbox
			WHERE channel_id = 'x' AND state IN ('received', 'ready')
			  AND (next_attempt_at IS NULL OR next_attempt_at <= clock_timestamp())
			ORDER BY chat_key, ingress_seq LIMIT 100 FOR UPDATE SKIP LOCKED`,
		"agent_run claim scan": `EXPLAIN SELECT * FROM agent_run
			WHERE state = 'queued' ORDER BY enqueue_seq LIMIT 50 FOR UPDATE SKIP LOCKED`,
		"agent_run reaper scan": `EXPLAIN SELECT r.* FROM agent_run r
			JOIN ctx_session_execution e ON e.session_id = r.session_id AND e.run_id = r.id
			WHERE r.state = 'running' AND e.lease_until <= clock_timestamp()
			ORDER BY r.enqueue_seq LIMIT 100 FOR UPDATE OF r SKIP LOCKED`,
		"channel_outbox due scan": `EXPLAIN SELECT * FROM channel_outbox
			WHERE channel_id = 'x' AND state = 'pending'
			  AND (next_attempt_at IS NULL OR next_attempt_at <= clock_timestamp())
			ORDER BY delivery_key, operation_index LIMIT 100 FOR UPDATE SKIP LOCKED`,
		"channel_outbox attempt expiry": `EXPLAIN SELECT * FROM channel_outbox
			WHERE state = 'sending' AND attempt_started_at <= clock_timestamp() - interval '5 minutes'
			ORDER BY attempt_started_at LIMIT 100 FOR UPDATE SKIP LOCKED`,
		"channel claimable scan": `EXPLAIN SELECT * FROM channel
			WHERE enabled AND (runtime_lease_until IS NULL OR runtime_lease_until <= clock_timestamp())
			ORDER BY id LIMIT 50 FOR UPDATE SKIP LOCKED`,
	}
	for name, q := range queries {
		rows, err := conn.Query(ctx, q)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var plan strings.Builder
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				t.Fatal(err)
			}
			plan.WriteString(line + "\n")
		}
		rows.Close()
		if !strings.Contains(plan.String(), "Index") {
			t.Fatalf("%s: plan has no index:\n%s", name, plan.String())
		}
	}
}
