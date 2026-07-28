//go:build system

package system

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	apitypes "github.com/CherryHQ/stella/api/types"
)

// testSchedulerRestart schedules a one-time chat, takes the candidate and its
// embedded PostgreSQL offline before the due time, and restarts only after the
// timestamp has passed. The restored worker must execute the durable job once,
// persist one successful run, and retire the one-time schedule.
func (h *harness) testSchedulerRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	fake := newFakeAnthropic(t)
	reply := "scheduler restart reply " + h.runID
	fake.enqueueText(reply)

	const modelID = "claude-sonnet-4-6"
	providerID := h.createNamedFakeProvider(t, ctx, "anthropic-scheduler-"+h.runID, fake.baseURL())
	agentID := h.createNamedAgent(t, ctx, "sys-test-scheduler-agent-"+h.runID, providerID+"/"+modelID)

	fireAt := time.Now().UTC().Add(10 * time.Second)
	resp := h.postJSON(t, ctx, fmt.Sprintf("/api/agents/%s/scheduler/jobs", agentID), map[string]any{
		"name":         "restart-once-" + h.runID,
		"message":      "run after restart " + h.runID,
		"at":           fireAt.Format(time.RFC3339Nano),
		"session_mode": "new",
	})
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST scheduler job = %d, want %d\n%s", resp.StatusCode, http.StatusCreated, h.proc.logTail(40))
	}
	var job apitypes.Job
	if err := json.NewDecoder(resp.Body).Decode(&job); err != nil {
		t.Fatalf("decode scheduler job: %v", err)
	}
	if job.Id == "" || !job.Enabled {
		t.Fatalf("created scheduler job = %+v, want enabled job with id", job)
	}

	// Stop before the due time, proving no in-memory timer or pre-restart model
	// request can satisfy the journey.
	h.proc.stop(t)
	h.db.Close()
	h.db = nil
	if got := len(fake.requests()); got != 0 {
		t.Fatalf("fake received %d request(s) before restart, want 0", got)
	}

	wait := time.Until(fireAt.Add(250 * time.Millisecond))
	if wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			t.Fatalf("waiting for offline scheduler timestamp: %v", ctx.Err())
		}
	}

	proc, baseURL := startServer(t, h.runID+"-scheduler-restart", h.home, h.vaultKey)
	h.proc = proc
	h.baseURL = baseURL
	db, err := pgxpool.New(ctx, embeddedDSN(t, h.home))
	if err != nil {
		t.Fatalf("connect embedded PostgreSQL after scheduler restart: %v", err)
	}
	h.db = db

	runsPath := fmt.Sprintf("/api/agents/%s/scheduler/jobs/%s/runs?page_size=10", agentID, job.Id)
	deadline := time.Now().Add(60 * time.Second)
	var runs apitypes.JobRunList
	for {
		runs = h.getSchedulerRuns(t, ctx, runsPath)
		if len(runs.Runs) == 1 && runs.Runs[0].Status == "success" {
			break
		}
		if len(runs.Runs) > 1 {
			t.Fatalf("scheduler job produced %d runs, want exactly one", len(runs.Runs))
		}
		if len(runs.Runs) == 1 && runs.Runs[0].Status == "error" {
			t.Fatalf("scheduler run failed: %+v\n%s", runs.Runs[0], h.proc.logTail(60))
		}
		if time.Now().After(deadline) {
			t.Fatalf("scheduler run did not succeed after restart; last runs=%+v\n%s", runs.Runs, h.proc.logTail(60))
		}
		time.Sleep(200 * time.Millisecond)
	}
	if runs.Runs[0].Output == nil || *runs.Runs[0].Output != reply {
		t.Fatalf("scheduler output = %v, want %q", runs.Runs[0].Output, reply)
	}

	// Give retirement and any accidental duplicate enqueue a bounded window,
	// then assert both durable and provider-facing exactly-once behavior.
	time.Sleep(time.Second)
	runs = h.getSchedulerRuns(t, ctx, runsPath)
	if len(runs.Runs) != 1 {
		t.Fatalf("scheduler job produced %d durable runs after settling, want 1", len(runs.Runs))
	}
	if got := len(fake.requests()); got != 1 {
		t.Fatalf("fake received %d scheduler model requests, want exactly 1", got)
	}
	job = h.getSchedulerJob(t, ctx, agentID, job.Id)
	if job.Enabled {
		t.Fatal("one-time scheduler job remains enabled after successful fire")
	}
}

func (h *harness) getSchedulerRuns(t *testing.T, ctx context.Context, path string) apitypes.JobRunList {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.baseURL+path, nil)
	if err != nil {
		t.Fatalf("build scheduler runs request: %v", err)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("GET scheduler runs: %v\n%s", err, h.proc.logTail(40))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		t.Fatalf("GET scheduler runs = %d, want %d\n%s", resp.StatusCode, http.StatusOK, h.proc.logTail(40))
	}
	var runs apitypes.JobRunList
	if err := json.NewDecoder(resp.Body).Decode(&runs); err != nil {
		t.Fatalf("decode scheduler runs: %v", err)
	}
	return runs
}

func (h *harness) getSchedulerJob(t *testing.T, ctx context.Context, agentID, jobID string) apitypes.Job {
	t.Helper()
	path := fmt.Sprintf("/api/agents/%s/scheduler/jobs/%s", agentID, jobID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.baseURL+path, nil)
	if err != nil {
		t.Fatalf("build scheduler job request: %v", err)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("GET scheduler job: %v\n%s", err, h.proc.logTail(40))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET scheduler job = %d, want %d\n%s", resp.StatusCode, http.StatusOK, h.proc.logTail(40))
	}
	var job apitypes.Job
	if err := json.NewDecoder(resp.Body).Decode(&job); err != nil {
		t.Fatalf("decode scheduler job: %v", err)
	}
	return job
}
