//go:build system

package system

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/test/testbed"
)

// fakePlatform is the test-controlled platform endpoint the testchan adapter
// talks to: /poll hands out pending events (non-destructive, so the adapter's
// re-poll exercises durable dedup), /send records every outbound operation.
type fakePlatform struct {
	srv    *httptest.Server
	mu     sync.Mutex
	events []map[string]string
	acked  map[string]bool
	sends  []map[string]any
	// failOps lists delivery keys whose /send must return 500 until cleared —
	// used to hold a send open across a replica crash.
	failOps map[string]bool
}

func newFakePlatform(t *testing.T) *fakePlatform {
	t.Helper()
	fp := &fakePlatform{acked: map[string]bool{}, failOps: map[string]bool{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/poll", func(w http.ResponseWriter, _ *http.Request) {
		fp.mu.Lock()
		defer fp.mu.Unlock()
		pending := []map[string]string{}
		for _, ev := range fp.events {
			if !fp.acked[ev["id"]] {
				pending = append(pending, ev)
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"events": pending})
	})
	mux.HandleFunc("/send", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		fp.mu.Lock()
		defer fp.mu.Unlock()
		if key, _ := body["op_key"].(string); key != "" && fp.failOps[key] {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		fp.sends = append(fp.sends, body)
		_ = json.NewEncoder(w).Encode(map[string]any{"platform_message_id": fmt.Sprintf("pm-%d", len(fp.sends))})
	})
	fp.srv = httptest.NewServer(mux)
	t.Cleanup(fp.srv.Close)
	return fp
}

func (fp *fakePlatform) endpoint() string { return fp.srv.URL }

func (fp *fakePlatform) push(ev map[string]string) {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	fp.events = append(fp.events, ev)
}

func (fp *fakePlatform) ack(id string) {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	fp.acked[id] = true
}

func (fp *fakePlatform) failSend(opKey string) {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	fp.failOps[opKey] = true
}

func (fp *fakePlatform) allowSend(opKey string) {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	delete(fp.failOps, opKey)
}

func (fp *fakePlatform) sendCount() int {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	return len(fp.sends)
}

func (fp *fakePlatform) lastSend() map[string]any {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	if len(fp.sends) == 0 {
		return nil
	}
	return fp.sends[len(fp.sends)-1]
}

// durableHarness is a harness whose stellad runs the durable channel path and
// the test adapter. Mirrors newHarness but with the required ExtraEnv.
func newDurableHarness(t *testing.T) *harness {
	t.Helper()
	skipUnsupportedHost(t)
	runID := newRunID(t)
	instance, err := testbed.Start(context.Background(), testbed.Options{
		RepoRoot:  repoRoot(t),
		FakeModel: true,
		Bootstrap: false,
		ExtraEnv: map[string]string{
			"STELLA_TEST_CHANNELS":           "1",
			"STELLA_CHANNEL_DURABLE_INGRESS": "1",
		},
	})
	if err != nil {
		t.Fatalf("system: start durable testbed: %v", err)
	}
	t.Cleanup(func() {
		if err := instance.Stop(); err != nil {
			t.Errorf("system: stop testbed: %v", err)
		}
	})
	db, err := pgxpool.New(context.Background(), instance.DatabaseURL())
	if err != nil {
		t.Fatalf("system: connect assertion pool: %v", err)
	}
	t.Cleanup(db.Close)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("system: cookie jar: %v", err)
	}
	h := &harness{owner: t, runID: runID, baseURL: instance.BaseURL(), client: &http.Client{Jar: jar}, db: db, proc: instance}
	sharedFake = instance
	h.registerBootstrapUser(t, context.Background())
	return h
}

func (h *harness) createTestChannel(t *testing.T, ctx context.Context, agentID, endpoint, botName string) string {
	t.Helper()
	cfg, _ := json.Marshal(map[string]any{
		"endpoint":          endpoint,
		"bot_name":          botName,
		"poll_interval_ms":  150,
		"allow_dm":          true,
		"allow_unlinked_dm": true,
	})
	resp := h.postJSON(t, ctx, "/api/channels", map[string]any{
		"type":     "testchan",
		"agent_id": agentID,
		"enabled":  true,
		"config":   string(cfg),
	})
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/channels = %d, want %d\n%s", resp.StatusCode, http.StatusCreated, h.proc.LogTail(60))
	}
	var created struct {
		ID      string `json:"id"`
		Enabled bool   `json:"enabled"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode channel: %v", err)
	}
	// Channels create disabled by design; enable via PATCH.
	enResp := h.patchJSON(t, ctx, "/api/channels/"+created.ID, map[string]any{"enabled": true})
	_ = enResp.Body.Close()
	if enResp.StatusCode != http.StatusOK {
		t.Fatalf("PATCH enable channel = %d, want 200", enResp.StatusCode)
	}
	return created.ID
}

func waitForCond(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// dumpDurableState logs the durable pipeline tables for failure diagnosis.
func (h *harness) dumpDurableState(t *testing.T, ctx context.Context) {
	t.Helper()
	rows2, err := h.db.Query(ctx, "SELECT id, enabled, runtime_state, runtime_error_code, runtime_owner_id FROM channel")
	if err == nil {
		for rows2.Next() {
			var id, state string
			var enabled bool
			var errCode, owner *string
			_ = rows2.Scan(&id, &enabled, &state, &errCode, &owner)
			t.Logf("channel %s enabled=%v state=%s err=%v owner=%v", id, enabled, state, errCode, owner)
		}
		rows2.Close()
	}
	ir, err := h.db.Query(ctx, "SELECT id, state, error_code, event_kind FROM channel_inbox")
	if err == nil {
		for ir.Next() {
			var id, state, kind string
			var code *string
			_ = ir.Scan(&id, &state, &code, &kind)
			t.Logf("inbox %s state=%s kind=%s code=%v", id, state, kind, code)
		}
		ir.Close()
	}
	ors, err := h.db.Query(ctx, "SELECT id, state, COALESCE(error_code,''), COALESCE(attempt_started_at::text,'') FROM channel_outbox")
	if err == nil {
		for ors.Next() {
			var id, state, lastErr, started string
			_ = ors.Scan(&id, &state, &lastErr, &started)
			t.Logf("outbox %s state=%s started=%s err=%s", id, state, started, lastErr)
		}
		ors.Close()
	}
	for _, table := range []string{"channel_inbox", "agent_run", "channel_outbox"} {
		var n int
		if err := h.db.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&n); err != nil {
			t.Logf("%s: query failed: %v", table, err)
			continue
		}
		t.Logf("%s: %d rows", table, n)
	}
	rows, err := h.db.Query(ctx, "SELECT id, state, last_error FROM agent_run")
	if err == nil {
		for rows.Next() {
			var id, state string
			var lastErr *string
			_ = rows.Scan(&id, &state, &lastErr)
			t.Logf("run %s state=%s err=%v", id, state, lastErr)
		}
		rows.Close()
	}
	t.Logf("server log tail:\n%s", h.proc.LogTail(30))
}

// M1: a real stellad process, a real adapter, a real database — event in,
// reply out through the durable inbox → run → outbox chain.
func TestDurableChannelLoop(t *testing.T) {
	h := newDurableHarness(t)
	fake := newFakeAnthropic(t)
	fp := newFakePlatform(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	const modelID = "claude-sonnet-4-6"
	reply := "DURABLE " + h.runID
	fake.enqueueText(reply)
	providerID := h.createWebhookFakeProvider(t, ctx, fake.baseURL(), "-durable")
	agentID := h.createWebhookAgent(t, ctx, providerID+"/"+modelID, "-durable")
	botName := "testbot-" + h.runID
	h.createTestChannel(t, ctx, agentID, fp.endpoint(), botName)

	fp.push(map[string]string{
		"id": "e1-" + h.runID, "chat_id": "chat-1", "sender_id": "user-1",
		"sender_name": "U1", "text": "hello",
	})

	// The reply must arrive at the fake platform exactly once, from the bot.
	if !waitForCondOK(90*time.Second, func() bool { return fp.sendCount() >= 1 }) {
		h.dumpDurableState(t, ctx)
		t.Fatal("timed out waiting for durable reply send")
	}
	sent := fp.lastSend()
	if sent["text"] != reply {
		t.Fatalf("sent text = %v, want %q", sent["text"], reply)
	}
	if sent["account"] != botName {
		t.Fatalf("sent account = %v, want %q", sent["account"], botName)
	}
	if sent["chat_key"] != "chat-1" {
		t.Fatalf("sent chat_key = %v, want chat-1", sent["chat_key"])
	}

	// Exactly one inbox fact, one completed run, one sent outbox op.
	var inbox, runs, outboxSent int
	if err := h.db.QueryRow(ctx, "SELECT count(*) FROM channel_inbox").Scan(&inbox); err != nil {
		t.Fatal(err)
	}
	if err := h.db.QueryRow(ctx, "SELECT count(*) FROM agent_run").Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if err := h.db.QueryRow(ctx, "SELECT count(*) FROM channel_outbox WHERE state='sent'").Scan(&outboxSent); err != nil {
		t.Fatal(err)
	}
	if inbox != 1 || runs != 1 || outboxSent != 1 {
		t.Fatalf("inbox=%d runs=%d outbox_sent=%d, want 1/1/1", inbox, runs, outboxSent)
	}
	// The real model saw exactly one request — no replayed turn.
	if got := len(fake.requests()); got != 1 {
		t.Fatalf("model requests = %d, want 1", got)
	}

	// Redelivery of the same platform event must not create a second run —
	// poll keeps returning the un-acked event; dedup must hold.
	time.Sleep(2 * time.Second)
	if err := h.db.QueryRow(ctx, "SELECT count(*) FROM agent_run").Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if runs != 1 || fp.sendCount() != 1 {
		t.Fatalf("redelivery created work: runs=%d sends=%d", runs, fp.sendCount())
	}
	fp.ack("e1-" + h.runID)

	// /new routes as a command run and its confirmation comes back through
	// the outbox — the command path works on durable ingress too.
	fake.enqueueText("should not be used")
	fp.push(map[string]string{
		"id": "e2-" + h.runID, "chat_id": "chat-1", "sender_id": "user-1",
		"sender_name": "U1", "command": "new",
	})
	waitForCond(t, 60*time.Second, "new-command reply", func() bool {
		return fp.sendCount() >= 2
	})
}

// M1: the receiving replica dies after the run completed but before the send
// landed; restart must recover the send without re-executing the run.
func TestDurableChannelRestartBeforeSend(t *testing.T) {
	h := newDurableHarness(t)
	fake := newFakeAnthropic(t)
	fp := newFakePlatform(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	const modelID = "claude-sonnet-4-6"
	reply := "RESTART " + h.runID
	fake.enqueueText(reply)
	providerID := h.createWebhookFakeProvider(t, ctx, fake.baseURL(), "-restart")
	agentID := h.createWebhookAgent(t, ctx, providerID+"/"+modelID, "-restart")
	botName := "testbot-" + h.runID
	h.createTestChannel(t, ctx, agentID, fp.endpoint(), botName)

	fp.push(map[string]string{
		"id": "e3-" + h.runID, "chat_id": "chat-2", "sender_id": "user-1",
		"sender_name": "U1", "text": "hello",
	})
	// Wait until the run has committed its outbox op, then hold the send.
	var runID string
	if !waitForCondOK(90*time.Second, func() bool {
		return h.db.QueryRow(ctx,
			"SELECT r.id FROM agent_run r WHERE r.state='completed' AND EXISTS (SELECT 1 FROM channel_outbox o WHERE o.run_id=r.id)").Scan(&runID) == nil
	}) {
		h.dumpDurableState(t, ctx)
		t.Fatal("timed out waiting for run completed with pending outbox")
	}
	// Find the pending op's delivery key and fail its send.
	var opKey string
	if err := h.db.QueryRow(ctx, "SELECT delivery_key FROM channel_outbox WHERE run_id=$1 LIMIT 1", runID).Scan(&opKey); err != nil {
		t.Fatal(err)
	}
	fp.failSend(opKey)

	// Kill the process before the send can succeed; restart it.
	if err := h.proc.Kill(); err != nil {
		t.Fatalf("kill replica: %v", err)
	}
	if err := h.proc.Restart(ctx); err != nil {
		t.Fatalf("restart replica: %v", err)
	}
	fp.allowSend(opKey)

	waitForCond(t, 90*time.Second, "post-restart send", func() bool {
		return fp.sendCount() >= 1
	})
	if got := fp.lastSend()["text"]; got != reply {
		t.Fatalf("recovered send text = %v, want %q", got, reply)
	}
	// No re-execution: the model still saw exactly one request.
	if got := len(fake.requests()); got != 1 {
		t.Fatalf("model requests after restart = %d, want 1", got)
	}
	var sentOps int
	if err := h.db.QueryRow(ctx, "SELECT count(*) FROM channel_outbox WHERE run_id=$1 AND state='sent'", runID).Scan(&sentOps); err != nil {
		t.Fatal(err)
	}
	if sentOps != 1 {
		t.Fatalf("sent ops = %d, want 1", sentOps)
	}
}

func waitForCondOK(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}

func (h *harness) patchJSON(t *testing.T, ctx context.Context, path string, body any) *http.Response {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request body: %v", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, h.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("PATCH %s: %v", path, err)
	}
	return resp
}

// startReplica boots an extra replica sharing the first instance's embedded
// cluster — same DSN + vault key, like StartReplicas but ordered so the test
// can pin roles deterministically.
func startReplica(t *testing.T, first *testbed.Instance, extra map[string]string) *testbed.Instance {
	t.Helper()
	opts := testbed.Options{
		RepoRoot:    repoRoot(t),
		DatabaseURL: first.DatabaseURL(),
		VaultKey:    first.VaultKey(),
		Bootstrap:   false,
		Managed:     false,
		ExtraEnv:    extra,
	}
	inst, err := testbed.Start(context.Background(), opts)
	if err != nil {
		t.Fatalf("start replica: %v", err)
	}
	t.Cleanup(func() {
		if err := inst.Stop(); err != nil {
			t.Errorf("stop replica: %v", err)
		}
	})
	return inst
}

func replicaIDOf(t *testing.T, inst *testbed.Instance) string {
	t.Helper()
	log := inst.LogTail(200)
	idx := strings.LastIndex(log, "replica_id=")
	if idx < 0 {
		t.Fatalf("replica identity not logged\n%s", log)
	}
	rest := log[idx+len("replica_id="):]
	if cut := strings.IndexAny(rest, " \n"); cut >= 0 {
		rest = rest[:cut]
	}
	return rest
}

// M2: four real processes share one database — D hosts it (and the API),
// A owns the channel, B runs the only worker, C waits for the lease.
// Assertions bind roles to actual process identities, not store instances.
func TestDurableChannelThreeReplicas(t *testing.T) {
	skipUnsupportedHost(t)
	fp := newFakePlatform(t)
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	baseEnv := map[string]string{
		"STELLA_TEST_CHANNELS":           "1",
		"STELLA_CHANNEL_DURABLE_INGRESS": "1",
	}
	// D: database owner + API + bootstrap, never a channel owner or worker.
	d, err := testbed.Start(ctx, testbed.Options{RepoRoot: repoRoot(t), FakeModel: true, Bootstrap: false, ExtraEnv: mergeEnvs(baseEnv, map[string]string{
		"STELLA_CHANNEL_LEASE": "off", "STELLA_RUN_WORKER": "off", "STELLA_TESTCHAN_TAG": "D",
	})})
	if err != nil {
		t.Fatalf("start D: %v", err)
	}
	t.Cleanup(func() { _ = d.Stop() })
	sharedFake = d
	fake := newFakeAnthropic(t)
	h := &harness{owner: t, runID: newRunID(t), baseURL: d.BaseURL(), client: mustCookieClient(t), proc: d}
	h.registerBootstrapUser(t, ctx)
	db, err := pgxpool.New(ctx, d.DatabaseURL())
	if err != nil {
		t.Fatal(err)
	}
	h.db = db
	t.Cleanup(db.Close)

	const modelID = "claude-sonnet-4-6"
	fake.enqueueText("ONE " + h.runID)
	fake.enqueueText("TWO " + h.runID)
	providerID := h.createWebhookFakeProvider(t, ctx, fake.baseURL(), "-m2")
	agentID := h.createWebhookAgent(t, ctx, providerID+"/"+modelID, "-m2")
	botName := "testbot-" + h.runID
	channelID := h.createTestChannel(t, ctx, agentID, fp.endpoint(), botName)

	// A: channel owner + receiver, no worker.
	a := startReplica(t, d, mergeEnvs(baseEnv, map[string]string{
		"STELLA_RUN_WORKER": "off", "STELLA_TESTCHAN_TAG": "A",
	}))
	aID := replicaIDOf(t, a)
	waitForCond(t, 60*time.Second, "A owns the channel", func() bool {
		var owner string
		return db.QueryRow(ctx, "SELECT COALESCE(runtime_owner_id,'') FROM channel WHERE id=$1", channelID).Scan(&owner) == nil && owner == aID
	})

	// B: the only worker. C: lease contender but no worker.
	b := startReplica(t, d, mergeEnvs(baseEnv, map[string]string{
		"STELLA_CHANNEL_LEASE": "off", "STELLA_TESTCHAN_TAG": "B",
	}))
	c := startReplica(t, d, mergeEnvs(baseEnv, map[string]string{
		"STELLA_RUN_WORKER": "off", "STELLA_TESTCHAN_TAG": "C",
	}))
	cID := replicaIDOf(t, c)

	// Event 1: A polls it in, B executes it, A sends the reply.
	fp.push(map[string]string{
		"id": "m2-e1-" + h.runID, "chat_id": "chat-m2", "sender_id": "user-1", "sender_name": "U", "text": "first",
	})
	waitForCond(t, 120*time.Second, "first reply sent by A", func() bool {
		s := fp.lastSend()
		return s != nil && s["tag"] == "A"
	})
	var workerID string
	if err := db.QueryRow(ctx, "SELECT COALESCE(worker_id,'') FROM agent_run WHERE state='completed'").Scan(&workerID); err != nil {
		t.Fatal(err)
	}
	if bpid := b.PID(); bpid == 0 || !strings.Contains(workerID, fmt.Sprintf("-%d-", bpid)) {
		t.Fatalf("run worker_id=%q does not name B's process (pid %d)", workerID, bpid)
	}
	if got := len(fake.requests()); got != 1 {
		t.Fatalf("model requests = %d, want 1", got)
	}
	fp.ack("m2-e1-" + h.runID)

	// Kill A mid-flight setup: C is the only lease participant left and must
	// take ownership, then receive and send the next event.
	if err := a.Kill(); err != nil {
		t.Fatalf("kill A: %v", err)
	}
	waitForCond(t, 90*time.Second, "C takes over the channel lease", func() bool {
		var owner string
		return db.QueryRow(ctx, "SELECT COALESCE(runtime_owner_id,'') FROM channel WHERE id=$1", channelID).Scan(&owner) == nil && owner == cID
	})
	fp.push(map[string]string{
		"id": "m2-e2-" + h.runID, "chat_id": "chat-m2", "sender_id": "user-1", "sender_name": "U", "text": "second",
	})
	waitForCond(t, 120*time.Second, "second reply sent by C", func() bool {
		s := fp.lastSend()
		return s != nil && s["tag"] == "C" && s["text"] == "TWO "+h.runID
	})

	// Old owner resumes: it must never own or send again while C's lease lives.
	if err := a.Restart(ctx); err != nil {
		t.Fatalf("restart A: %v", err)
	}
	time.Sleep(4 * time.Second)
	var ownerNow string
	if err := db.QueryRow(ctx, "SELECT COALESCE(runtime_owner_id,'') FROM channel WHERE id=$1", channelID).Scan(&ownerNow); err != nil {
		t.Fatal(err)
	}
	if ownerNow != cID {
		t.Fatalf("old owner took the lease back: owner=%q, want %q", ownerNow, cID)
	}
	for _, s := range fp.sendsAll() {
		if s["tag"] == "A" && s["text"] == "TWO "+h.runID {
			t.Fatal("stale owner A sent a post-takeover operation")
		}
	}
	if got := len(fake.requests()); got != 2 {
		t.Fatalf("model requests = %d, want 2 (no replayed execution)", got)
	}
}

func mergeEnvs(base, extra map[string]string) map[string]string {
	out := map[string]string{}
	maps.Copy(out, base)
	maps.Copy(out, extra)
	return out
}

func mustCookieClient(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Jar: jar}
}

func (fp *fakePlatform) sendsAll() []map[string]any {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	return append([]map[string]any(nil), fp.sends...)
}
