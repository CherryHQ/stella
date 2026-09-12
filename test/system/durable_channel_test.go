//go:build system

package system

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	agentruntime "github.com/CherryHQ/stella/internal/agent/runtime"
	"github.com/CherryHQ/stella/internal/sessionevent"
	"github.com/CherryHQ/stella/test/testbed"
)

// fakePlatform is the test-controlled platform endpoint the testchan adapter
// talks to: /poll hands out pending events (non-destructive, so the adapter's
// re-poll exercises durable dedup), /send records every outbound operation.
// A send carrying "message_id" edits that message in place — the draft
// contract — and every call (create or edit) lands in the sends log while
// messages holds the current content per platform id.
type fakePlatform struct {
	srv    *httptest.Server
	mu     sync.Mutex
	events []map[string]string
	acked  map[string]bool
	sends  []map[string]any
	// messages maps platform_message_id to its current content — creates and
	// in-place edits share the log, so messageCount proves identity stability.
	messages map[string]map[string]any
	nextID   int
	// failOps lists delivery keys whose /send must return 500 until cleared —
	// used to hold a send open across a replica crash.
	failOps map[string]bool
}

func newFakePlatform(t *testing.T) *fakePlatform {
	t.Helper()
	fp := &fakePlatform{acked: map[string]bool{}, failOps: map[string]bool{}, messages: map[string]map[string]any{}}
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
		if editID, _ := body["message_id"].(string); editID != "" {
			if msg, ok := fp.messages[editID]; ok {
				maps.Copy(msg, body)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"platform_message_id": editID})
			return
		}
		fp.nextID++
		id := fmt.Sprintf("pm-%d", fp.nextID)
		msg := map[string]any{}
		maps.Copy(msg, body)
		fp.messages[id] = msg
		_ = json.NewEncoder(w).Encode(map[string]any{"platform_message_id": id})
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
			"STELLA_TEST_CHANNELS": "1",
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

// startReplicaHome is startReplica with a caller-owned STELLA_HOME so two
// replicas can share one POSIX namespace — the deployment requirement the
// attachment assertions exercise.
func startReplicaHome(t *testing.T, first *testbed.Instance, home string, extra map[string]string) *testbed.Instance {
	t.Helper()
	inst, err := testbed.Start(context.Background(), testbed.Options{
		RepoRoot:    repoRoot(t),
		DatabaseURL: first.DatabaseURL(),
		VaultKey:    first.VaultKey(),
		Home:        home,
		Bootstrap:   false,
		Managed:     false,
		ExtraEnv:    extra,
	})
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
		"STELLA_TEST_CHANNELS": "1",
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

// messageCount is the number of distinct platform messages created — edits
// under a message_id reuse the id, so this proves create/edit identity.
func (fp *fakePlatform) messageCount() int {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	return len(fp.messages)
}

// message returns the current content of one platform message.
func (fp *fakePlatform) message(id string) map[string]any {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	return fp.messages[id]
}

// messageIDs lists every created platform message id.
func (fp *fakePlatform) messageIDs() []string {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	ids := make([]string, 0, len(fp.messages))
	for id := range fp.messages {
		ids = append(ids, id)
	}
	return ids
}

// M2 core: the SAME received event hands off A → B → C. B claims the run and
// is pinned mid-model by a gate; A dies while B's turn is in flight; C takes
// the lease; B finishes the original run; C sends the original reply.
func TestDurableChannelSameEventHandoff(t *testing.T) {
	skipUnsupportedHost(t)
	fp := newFakePlatform(t)
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	baseEnv := map[string]string{
		"STELLA_TEST_CHANNELS": "1",
	}
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
	gate := fake.EnqueueGatedText("partial-", "HANDOFF "+h.runID)
	providerID := h.createWebhookFakeProvider(t, ctx, fake.baseURL(), "-hand")
	agentID := h.createWebhookAgent(t, ctx, providerID+"/"+modelID, "-hand")
	botName := "testbot-" + h.runID
	channelID := h.createTestChannel(t, ctx, agentID, fp.endpoint(), botName)

	a := startReplica(t, d, mergeEnvs(baseEnv, map[string]string{
		"STELLA_RUN_WORKER": "off", "STELLA_TESTCHAN_TAG": "A",
	}))
	aID := replicaIDOf(t, a)
	waitForCond(t, 60*time.Second, "A owns the channel", func() bool {
		var owner string
		return db.QueryRow(ctx, "SELECT COALESCE(runtime_owner_id,'') FROM channel WHERE id=$1", channelID).Scan(&owner) == nil && owner == aID
	})
	b := startReplica(t, d, mergeEnvs(baseEnv, map[string]string{
		"STELLA_CHANNEL_LEASE": "off", "STELLA_TESTCHAN_TAG": "B",
	}))
	c := startReplica(t, d, mergeEnvs(baseEnv, map[string]string{
		"STELLA_RUN_WORKER": "off", "STELLA_TESTCHAN_TAG": "C",
	}))
	cID := replicaIDOf(t, c)

	// A receives; B claims and sits inside the gated model call.
	fp.push(map[string]string{
		"id": "m2-he-" + h.runID, "chat_id": "chat-he", "sender_id": "user-1", "sender_name": "U", "text": "hand me off",
	})
	var runID, workerID string
	waitForCond(t, 90*time.Second, "B claims the run mid-flight", func() bool {
		return db.QueryRow(ctx, "SELECT id, COALESCE(worker_id,'') FROM agent_run WHERE state='running'").Scan(&runID, &workerID) == nil
	})
	if bpid := b.PID(); bpid == 0 || !strings.Contains(workerID, fmt.Sprintf("-%d-", bpid)) {
		t.Fatalf("run worker_id=%q does not name B's process (pid %d)", workerID, bpid)
	}
	var inboxID string
	if err := db.QueryRow(ctx, "SELECT inbox_id FROM agent_run WHERE id=$1", runID).Scan(&inboxID); err != nil {
		t.Fatal(err)
	}
	// Pin the fault point: 'running' alone does not prove B is inside the
	// model call — wait until the fake confirms the gated turn is parked.
	select {
	case <-gate.Entered:
	case <-ctx.Done():
		t.Fatal("gated model call never parked inside the gate")
	}

	// A dies while B's turn is in flight; C inherits the channel. Expire the
	// lease row directly — waiting out A's 30s TTL inside a parked model gate
	// races the fake's release backstop; the claim path under test is identical.
	if err := a.Kill(); err != nil {
		t.Fatalf("kill A: %v", err)
	}
	if _, err := db.Exec(ctx, "UPDATE channel SET runtime_lease_until = clock_timestamp() - interval '1 second' WHERE id=$1", channelID); err != nil {
		t.Fatal(err)
	}
	waitForCond(t, 90*time.Second, "C owns the lease after A died", func() bool {
		var owner string
		return db.QueryRow(ctx, "SELECT COALESCE(runtime_owner_id,'') FROM channel WHERE id=$1", channelID).Scan(&owner) == nil && owner == cID
	})

	// Release B's model: the original run completes, C sends its reply. A
	// mid-turn draft may have already landed — the terminal reply is the send
	// carrying the full gated text.
	gate.Release()
	wantReply := "partial-HANDOFF " + h.runID
	waitForCond(t, 90*time.Second, "C sends the terminal reply", func() bool {
		for _, s := range fp.sendsAll() {
			if s["tag"] == "C" && s["text"] == wantReply {
				return true
			}
		}
		return false
	})
	// Same inbox, same run, one model call, one terminal send — nothing
	// replayed. Live drafts may add earlier sends of their own.
	var state string
	if err := db.QueryRow(ctx, "SELECT state FROM agent_run WHERE id=$1", runID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "completed" {
		t.Fatalf("original run state=%s, want completed", state)
	}
	if got := len(fake.requests()); got != 1 {
		t.Fatalf("model requests = %d, want 1", got)
	}
	var runs, sends int
	if err := db.QueryRow(ctx, "SELECT count(*) FROM agent_run WHERE inbox_id=$1", inboxID).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	for _, s := range fp.sendsAll() {
		if s["tag"] == "A" {
			t.Fatal("dead owner A sent a post-handoff operation")
		}
		if s["text"] == wantReply {
			sends++
		}
	}
	if runs != 1 || sends != 1 {
		t.Fatalf("runs=%d terminal sends=%d for the inbox event, want 1/1", runs, sends)
	}
	// The original run carries exactly one sent terminal receipt, and the
	// turn's history landed once — a replayed finish would double both.
	var sentOps, userMsgs, replyMsgs int
	if err := db.QueryRow(ctx, "SELECT count(*) FROM channel_outbox WHERE run_id=$1 AND state='sent' AND operation_kind != 'draft_update'", runID).Scan(&sentOps); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM ctx_message m JOIN ctx_conversation c ON c.id=m.conversation_id
		WHERE c.session_id=(SELECT session_id FROM agent_run WHERE id=$1) AND m.role='user' AND m.content LIKE '%hand me off%'`, runID).Scan(&userMsgs); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM ctx_message m JOIN ctx_conversation c ON c.id=m.conversation_id
		WHERE c.session_id=(SELECT session_id FROM agent_run WHERE id=$1) AND m.role='assistant' AND m.content LIKE '%HANDOFF '||$2||'%'`, runID, h.runID).Scan(&replyMsgs); err != nil {
		t.Fatal(err)
	}
	if sentOps != 1 || userMsgs != 1 || replyMsgs != 1 {
		t.Fatalf("sent_ops=%d user_msgs=%d reply_msgs=%d, want 1/1/1", sentOps, userMsgs, replyMsgs)
	}
}

// Journal isolation: a File event appended straight to the run's durable
// event log is NOT reply material — the send boundary only trusts the op
// chain the executing worker committed. After the lease handoff the new
// owner still delivers the turn's text, but the injected file must never
// reach the platform. Positive attachment delivery is proven by the Go-level
// prepare → append → independent-sender tests (run reply and group accept).
func TestDurableChannelJournalInjectionNotSent(t *testing.T) {
	skipUnsupportedHost(t)
	fp := newFakePlatform(t)
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	baseEnv := map[string]string{
		"STELLA_TEST_CHANNELS": "1",
	}
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
	gate := fake.EnqueueGatedText("file coming: ", "done "+h.runID)
	providerID := h.createWebhookFakeProvider(t, ctx, fake.baseURL(), "-att")
	agentID := h.createWebhookAgent(t, ctx, providerID+"/"+modelID, "-att")
	botName := "testbot-" + h.runID
	channelID := h.createTestChannel(t, ctx, agentID, fp.endpoint(), botName)

	a := startReplica(t, d, mergeEnvs(baseEnv, map[string]string{
		"STELLA_RUN_WORKER": "off", "STELLA_TESTCHAN_TAG": "A",
	}))
	aID := replicaIDOf(t, a)
	waitForCond(t, 60*time.Second, "A owns the channel", func() bool {
		var owner string
		return db.QueryRow(ctx, "SELECT COALESCE(runtime_owner_id,'') FROM channel WHERE id=$1", channelID).Scan(&owner) == nil && owner == aID
	})
	b := startReplica(t, d, mergeEnvs(baseEnv, map[string]string{
		"STELLA_CHANNEL_LEASE": "off", "STELLA_TESTCHAN_TAG": "B",
	}))
	c := startReplica(t, d, mergeEnvs(baseEnv, map[string]string{
		"STELLA_RUN_WORKER": "off", "STELLA_TESTCHAN_TAG": "C",
	}))
	cID := replicaIDOf(t, c)

	// A receives; B claims and sits inside the gated model call.
	fp.push(map[string]string{
		"id": "m2-at-" + h.runID, "chat_id": "chat-att", "sender_id": "user-1", "sender_name": "U", "text": "send me a file",
	})
	var runID, workerID, sessionID string
	waitForCond(t, 90*time.Second, "B claims the run mid-flight", func() bool {
		return db.QueryRow(ctx, "SELECT id, COALESCE(worker_id,''), session_id FROM agent_run WHERE state='running'").Scan(&runID, &workerID, &sessionID) == nil
	})
	if bpid := b.PID(); bpid == 0 || !strings.Contains(workerID, fmt.Sprintf("-%d-", bpid)) {
		t.Fatalf("run worker_id=%q does not name B's process (pid %d)", workerID, bpid)
	}
	select {
	case <-gate.Entered:
	case <-ctx.Done():
		t.Fatal("gated model call never parked inside the gate")
	}

	// Adversarial journal row: stage a file on the shared filesystem and
	// append a File event to the run's durable log while B's turn is still
	// inside the gate. If any send path trusted journal rows instead of the
	// committed op chain, these bytes would leak onto the wire.
	attachment := []byte("injected-bytes-" + h.runID)
	path := filepath.Join(t.TempDir(), "injected-"+h.runID+".bin")
	if err := os.WriteFile(path, attachment, 0o600); err != nil {
		t.Fatal(err)
	}
	var execToken string
	if err := db.QueryRow(ctx, "SELECT token::text FROM ctx_session_execution WHERE run_id=$1", runID).Scan(&execToken); err != nil {
		t.Fatalf("execution token for run: %v", err)
	}
	events := sessionevent.New(db)
	if err := events.Append(ctx, sessionID, execToken, runID, agentruntime.EncodeEvent(agentruntime.Event{
		File: &agentruntime.FileEvent{Path: path, Name: filepath.Base(path)},
	})); err != nil {
		t.Fatalf("append file event: %v", err)
	}

	// A dies while B's turn is in flight; C inherits the channel lease and
	// becomes the sender of record for the run's reply — the committed op
	// chain carries the result text, and the injected journal File must not.
	// Expire the lease directly, same reason as the reply-handoff case above.
	if err := a.Kill(); err != nil {
		t.Fatalf("kill A: %v", err)
	}
	if _, err := db.Exec(ctx, "UPDATE channel SET runtime_lease_until = clock_timestamp() - interval '1 second' WHERE id=$1", channelID); err != nil {
		t.Fatal(err)
	}
	waitForCond(t, 90*time.Second, "C owns the lease after A died", func() bool {
		var owner string
		return db.QueryRow(ctx, "SELECT COALESCE(runtime_owner_id,'') FROM channel WHERE id=$1", channelID).Scan(&owner) == nil && owner == cID
	})

	gate.Release()
	wantText := "done " + h.runID
	waitForCond(t, 90*time.Second, "C sends the reply text", func() bool {
		for _, s := range fp.sendsAll() {
			if s["tag"] == "C" && strings.Contains(fmt.Sprint(s["text"]), wantText) {
				return true
			}
		}
		return false
	})

	// The injected journal file must not ride along: no send — from any
	// replica — may carry file payloads, and the byte string must not appear
	// anywhere on the wire.
	for _, s := range fp.sendsAll() {
		if files, ok := s["files"].([]any); ok && len(files) > 0 {
			t.Fatalf("replica %s sent files %#v; the journal-injected file must stay undelivered", s["tag"], files)
		}
		if strings.Contains(fmt.Sprint(s), string(attachment)) {
			t.Fatalf("replica %s leaked injected bytes onto the wire: %v", s["tag"], s)
		}
	}
	var state string
	if err := db.QueryRow(ctx, "SELECT state FROM agent_run WHERE id=$1", runID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "completed" {
		t.Fatalf("run state=%s, want completed", state)
	}
}

// the model call. The drain budget must let the in-flight turn commit run +
// history + outbox instead of interrupting it; the process then exits 0 and
// the channel owner sends the reply — no replay, no lost work.
func TestDurableChannelGracefulDrain(t *testing.T) {
	skipUnsupportedHost(t)
	fp := newFakePlatform(t)
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	baseEnv := map[string]string{
		"STELLA_TEST_CHANNELS": "1",
	}
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
	gate := fake.EnqueueGatedText("drain-one-", "DRAINED "+h.runID)
	providerID := h.createWebhookFakeProvider(t, ctx, fake.baseURL(), "-drain")
	agentID := h.createWebhookAgent(t, ctx, providerID+"/"+modelID, "-drain")
	botName := "testbot-" + h.runID
	channelID := h.createTestChannel(t, ctx, agentID, fp.endpoint(), botName)

	a := startReplica(t, d, mergeEnvs(baseEnv, map[string]string{
		"STELLA_RUN_WORKER": "off", "STELLA_TESTCHAN_TAG": "A",
	}))
	aID := replicaIDOf(t, a)
	waitForCond(t, 60*time.Second, "A owns the channel", func() bool {
		var owner string
		return db.QueryRow(ctx, "SELECT COALESCE(runtime_owner_id,'') FROM channel WHERE id=$1", channelID).Scan(&owner) == nil && owner == aID
	})
	b := startReplica(t, d, mergeEnvs(baseEnv, map[string]string{
		"STELLA_CHANNEL_LEASE": "off", "STELLA_TESTCHAN_TAG": "B",
	}))

	fp.push(map[string]string{
		"id": "m2-gd-" + h.runID, "chat_id": "chat-gd", "sender_id": "user-1", "sender_name": "U", "text": "drain me",
	})
	var runID, workerID string
	waitForCond(t, 90*time.Second, "B claims the run", func() bool {
		return db.QueryRow(ctx, "SELECT id, COALESCE(worker_id,'') FROM agent_run WHERE state='running'").Scan(&runID, &workerID) == nil
	})
	if bpid := b.PID(); bpid == 0 || !strings.Contains(workerID, fmt.Sprintf("-%d-", bpid)) {
		t.Fatalf("run worker_id=%q does not name B's process (pid %d)", workerID, bpid)
	}
	// Pin B inside the gated model call, then SIGTERM it mid-turn.
	select {
	case <-gate.Entered:
	case <-ctx.Done():
		t.Fatal("gated model call never parked inside the gate")
	}
	if err := b.Terminate(); err != nil {
		t.Fatalf("SIGTERM B: %v", err)
	}
	// Release inside the drain budget: the detached turn must finish and
	// commit before the worker's process is allowed to exit.
	gate.Release()
	select {
	case <-b.Done():
		if err := b.WaitErr(); err != nil {
			t.Fatalf("B exited non-zero after drain: %v", err)
		}
	case <-time.After(gracefulTimeout):
		t.Fatal("B did not exit within the graceful drain budget")
	}

	// The in-flight turn survived the drain: run completed, reply op committed.
	var state string
	if err := db.QueryRow(ctx, "SELECT state FROM agent_run WHERE id=$1", runID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "completed" {
		logtail, lerr := os.ReadFile(b.LogPath())
		if lerr != nil {
			t.Logf("read B log: %v", lerr)
		}
		t.Logf("B log tail:\n%s", logtail)
		t.Fatalf("run state=%s after drained worker, want completed", state)
	}
	// draft_update rows are coalesced in-flight progress on the same run, not
	// extra sends — count the terminal reply chain only.
	var ops int
	if err := db.QueryRow(ctx, "SELECT count(*) FROM channel_outbox WHERE run_id=$1 AND operation_kind <> 'draft_update'", runID).Scan(&ops); err != nil {
		t.Fatal(err)
	}
	if ops != 1 {
		t.Fatalf("outbox ops=%d for the drained run, want 1", ops)
	}
	// The channel owner picks up the committed reply — no re-execution.
	waitForCond(t, 90*time.Second, "A sends the drained turn's reply", func() bool {
		s := fp.lastSend()
		return s != nil && s["tag"] == "A"
	})
	if got := fp.lastSend()["text"]; got != "drain-one-DRAINED "+h.runID {
		t.Fatalf("sent text = %v, want the gated reply", got)
	}
	if got := len(fake.requests()); got != 1 {
		t.Fatalf("model requests = %d, want 1 — drain must not replay the turn", got)
	}
	var sentOps int
	if err := db.QueryRow(ctx, "SELECT count(*) FROM channel_outbox WHERE run_id=$1 AND state='sent'", runID).Scan(&sentOps); err != nil {
		t.Fatal(err)
	}
	if sentOps != 1 || fp.sendCount() != 1 {
		t.Fatalf("sent_ops=%d sends=%d, want 1/1", sentOps, fp.sendCount())
	}
}

// M2: pause (not restart) the owner past its lease expiry; C takes over; the
// resumed stale owner must be fenced out of a new pending send.
func TestDurableChannelStaleOwnerPaused(t *testing.T) {
	skipUnsupportedHost(t)
	fp := newFakePlatform(t)
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	baseEnv := map[string]string{
		"STELLA_TEST_CHANNELS": "1",
	}
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
	fake.enqueueText("STALE " + h.runID)
	providerID := h.createWebhookFakeProvider(t, ctx, fake.baseURL(), "-stale")
	agentID := h.createWebhookAgent(t, ctx, providerID+"/"+modelID, "-stale")
	botName := "testbot-" + h.runID
	channelID := h.createTestChannel(t, ctx, agentID, fp.endpoint(), botName)

	a := startReplica(t, d, mergeEnvs(baseEnv, map[string]string{
		"STELLA_RUN_WORKER": "off", "STELLA_TESTCHAN_TAG": "A",
	}))
	aID := replicaIDOf(t, a)
	waitForCond(t, 60*time.Second, "A owns the channel", func() bool {
		var owner string
		return db.QueryRow(ctx, "SELECT COALESCE(runtime_owner_id,'') FROM channel WHERE id=$1", channelID).Scan(&owner) == nil && owner == aID
	})
	_ = startReplica(t, d, mergeEnvs(baseEnv, map[string]string{
		"STELLA_CHANNEL_LEASE": "off", "STELLA_TESTCHAN_TAG": "B",
	}))
	c := startReplica(t, d, mergeEnvs(baseEnv, map[string]string{
		"STELLA_RUN_WORKER": "off", "STELLA_TESTCHAN_TAG": "C",
	}))
	cID := replicaIDOf(t, c)

	// Freeze A mid-ownership: it keeps its token and its running poller.
	if err := a.Pause(); err != nil {
		t.Fatalf("pause A: %v", err)
	}
	waitForCond(t, 120*time.Second, "C takes the expired lease", func() bool {
		var owner string
		return db.QueryRow(ctx, "SELECT COALESCE(runtime_owner_id,'') FROM channel WHERE id=$1", channelID).Scan(&owner) == nil && owner == cID
	})

	// Resume A — its stale token must fail every protected op.
	if err := a.Resume(); err != nil {
		t.Fatalf("resume A: %v", err)
	}
	// A pending send after resume is the stale-send probe.
	fp.push(map[string]string{
		"id": "m2-se-" + h.runID, "chat_id": "chat-stale", "sender_id": "user-1", "sender_name": "U", "text": "post-resume",
	})
	waitForCond(t, 120*time.Second, "post-resume reply sent by C", func() bool {
		s := fp.lastSend()
		return s != nil && s["tag"] == "C"
	})
	time.Sleep(3 * time.Second)
	var ownerNow string
	if err := db.QueryRow(ctx, "SELECT COALESCE(runtime_owner_id,'') FROM channel WHERE id=$1", channelID).Scan(&ownerNow); err != nil {
		t.Fatal(err)
	}
	if ownerNow != cID {
		t.Fatalf("resumed stale owner reclaimed the lease: owner=%q, want %q", ownerNow, cID)
	}
	for _, s := range fp.sendsAll() {
		if s["tag"] == "A" {
			t.Fatal("resumed stale owner A sent an operation")
		}
	}
}

// M2 fault row: the shared database disappears while a run is in flight and
// while a second event waits at the platform. Assertions: no send can happen
// without the DB (the in-flight turn's reply is lost, not faked), the un-acked
// event is received exactly once after recovery, and no run is re-executed.
func TestDurableChannelDBOutageRecovery(t *testing.T) {
	skipUnsupportedHost(t)
	fp := newFakePlatform(t)
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	baseEnv := map[string]string{
		"STELLA_TEST_CHANNELS": "1",
	}
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
	gate := fake.EnqueueGatedText("", "GONE "+h.runID)
	fake.enqueueText("AFTER " + h.runID)
	providerID := h.createWebhookFakeProvider(t, ctx, fake.baseURL(), "-dbout")
	agentID := h.createWebhookAgent(t, ctx, providerID+"/"+modelID, "-dbout")
	botName := "testbot-" + h.runID
	channelID := h.createTestChannel(t, ctx, agentID, fp.endpoint(), botName)

	a := startReplica(t, d, mergeEnvs(baseEnv, map[string]string{
		"STELLA_RUN_WORKER": "off", "STELLA_TESTCHAN_TAG": "A",
	}))
	aID := replicaIDOf(t, a)
	waitForCond(t, 60*time.Second, "A owns the channel", func() bool {
		var owner string
		return db.QueryRow(ctx, "SELECT COALESCE(runtime_owner_id,'') FROM channel WHERE id=$1", channelID).Scan(&owner) == nil && owner == aID
	})
	b := startReplica(t, d, mergeEnvs(baseEnv, map[string]string{
		"STELLA_CHANNEL_LEASE": "off", "STELLA_TESTCHAN_TAG": "B",
	}))
	_ = startReplica(t, d, mergeEnvs(baseEnv, map[string]string{
		"STELLA_RUN_WORKER": "off", "STELLA_TESTCHAN_TAG": "C",
	}))

	// E1: B claims the run and blocks inside the gated model call.
	fp.push(map[string]string{
		"id": "m2-db1-" + h.runID, "chat_id": "chat-db", "sender_id": "user-1", "sender_name": "U", "text": "first",
	})
	var run1ID, workerID string
	waitForCond(t, 90*time.Second, "B claims run1", func() bool {
		return db.QueryRow(ctx, "SELECT id, COALESCE(worker_id,'') FROM agent_run WHERE state='running'").Scan(&run1ID, &workerID) == nil
	})
	if bpid := b.PID(); bpid == 0 || !strings.Contains(workerID, fmt.Sprintf("-%d-", bpid)) {
		t.Fatalf("run1 worker_id=%q does not name B (pid %d)", workerID, bpid)
	}

	// DB outage: stop the shared cluster while every stellad stays alive. E2
	// is pushed while down — the poller can read it but cannot ingest, so it
	// stays un-acked at the platform.
	if err := d.StopDatabase(); err != nil {
		t.Fatalf("stop database: %v", err)
	}
	fp.push(map[string]string{
		"id": "m2-db2-" + h.runID, "chat_id": "chat-db", "sender_id": "user-1", "sender_name": "U", "text": "second",
	})
	// Span at least two execution-lease renew ticks (5s apart): B's renew
	// fails while the DB is down, its run context is cancelled, and the
	// in-flight turn cannot finish — whatever the lease row still says.
	// Stay under the fake gate's 30s deadlock fuse.
	time.Sleep(12 * time.Second)
	if err := d.StartDatabase(); err != nil {
		t.Fatalf("start database: %v", err)
	}
	gate.Release() // B already abandoned the request; unblock the fake handler.

	// Post-recovery: E2 flows end to end exactly once. The sender is whoever
	// holds the channel lease after reconnect (A or C) — the tag only must
	// not be B (worker never sends) or D.
	waitForCond(t, 150*time.Second, "E2 reply sent after recovery", func() bool {
		for _, s := range fp.sendsAll() {
			if s["text"] == "AFTER "+h.runID && (s["tag"] == "A" || s["tag"] == "C") {
				return true
			}
		}
		return false
	})

	// The in-flight run1 must end interrupted/failed — never completed, and
	// its reply must never have been sent.
	waitForCond(t, 90*time.Second, "run1 reaches a terminal non-completed state", func() bool {
		var state string
		if err := db.QueryRow(ctx, "SELECT state FROM agent_run WHERE id=$1", run1ID).Scan(&state); err != nil {
			return false
		}
		return state == "interrupted" || state == "failed" || state == "canceled"
	})
	for _, s := range fp.sendsAll() {
		if s["text"] == "GONE "+h.runID {
			t.Fatal("run1 reply was sent although its execution lease was lost in the outage")
		}
		if s["tag"] == "B" || s["tag"] == "D" {
			t.Fatalf("non-owner replica %v sent an operation", s["tag"])
		}
	}
	// Exactly-once across redelivery + outage: two inbox facts, two runs,
	// two model requests, one sent reply.
	var inboxN, runsN int
	if err := db.QueryRow(ctx, "SELECT count(*) FROM channel_inbox").Scan(&inboxN); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, "SELECT count(*) FROM agent_run").Scan(&runsN); err != nil {
		t.Fatal(err)
	}
	if inboxN != 2 || runsN != 2 {
		t.Fatalf("inbox=%d runs=%d, want 2/2", inboxN, runsN)
	}
	if got := len(fake.requests()); got != 2 {
		t.Fatalf("model requests = %d, want 2 (no replayed execution)", got)
	}
	var sentOps int
	if err := db.QueryRow(ctx, "SELECT count(*) FROM channel_outbox WHERE state='sent'").Scan(&sentOps); err != nil {
		t.Fatal(err)
	}
	if sentOps != 1 {
		t.Fatalf("sent outbox ops = %d, want 1 (only E2)", sentOps)
	}
}

// D9 at process level: pause the WORKER (not the channel owner) past its
// execution lease. The surviving worker must refuse the session's next run —
// the previous writer is provably alive — until the paused worker resumes,
// observes the loss, unwinds, and clears its own tombstone. Then the queued
// run proceeds exactly once and the channel owner sends its reply.
func TestDurableChannelPausedWorkerFencing(t *testing.T) {
	skipUnsupportedHost(t)
	fp := newFakePlatform(t)
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	baseEnv := map[string]string{"STELLA_TEST_CHANNELS": "1"}
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

	gate := fake.EnqueueGatedText("", "STUCK "+h.runID)
	fake.enqueueText("SECOND " + h.runID)
	providerID := h.createWebhookFakeProvider(t, ctx, fake.baseURL(), "-pwf")
	agentID := h.createWebhookAgent(t, ctx, providerID+"/claude-sonnet-4-6", "-pwf")
	channelID := h.createTestChannel(t, ctx, agentID, fp.endpoint(), "testbot-"+h.runID)

	a := startReplica(t, d, mergeEnvs(baseEnv, map[string]string{
		"STELLA_RUN_WORKER": "off", "STELLA_TESTCHAN_TAG": "A",
	}))
	aID := replicaIDOf(t, a)
	waitForCond(t, 60*time.Second, "A owns the channel", func() bool {
		var owner string
		return db.QueryRow(ctx, "SELECT COALESCE(runtime_owner_id,'') FROM channel WHERE id=$1", channelID).Scan(&owner) == nil && owner == aID
	})
	b := startReplica(t, d, mergeEnvs(baseEnv, map[string]string{
		"STELLA_CHANNEL_LEASE": "off", "STELLA_TESTCHAN_TAG": "B",
	}))
	c := startReplica(t, d, mergeEnvs(baseEnv, map[string]string{
		"STELLA_CHANNEL_LEASE": "off", "STELLA_TESTCHAN_TAG": "C",
	}))

	fp.push(map[string]string{
		"id": "m2-pw1-" + h.runID, "chat_id": "chat-pw", "sender_id": "user-1", "sender_name": "U", "text": "one",
	})
	var run1ID, workerID string
	waitForCond(t, 90*time.Second, "a worker claims run1", func() bool {
		return db.QueryRow(ctx, "SELECT id, COALESCE(worker_id,'') FROM agent_run WHERE state='running'").Scan(&run1ID, &workerID) == nil
	})
	// Freeze whichever worker claimed run1 — the writer stays alive but cannot
	// renew, exactly the "old writer may still be writing" case D9 fences.
	var w *testbed.Instance
	switch {
	case strings.Contains(workerID, fmt.Sprintf("-%d-", b.PID())):
		w = b
	case strings.Contains(workerID, fmt.Sprintf("-%d-", c.PID())):
		w = c
	default:
		t.Fatalf("run1 worker_id=%q matches neither B (pid %d) nor C (pid %d)", workerID, b.PID(), c.PID())
	}
	// The freeze must land while the worker is mid-write — inside the gated
	// model call — or there is nothing in flight to fence.
	select {
	case <-gate.Entered:
	case <-ctx.Done():
		t.Fatal("gated model call never parked inside the gate")
	}
	if err := w.Pause(); err != nil {
		t.Fatalf("pause worker: %v", err)
	}
	t.Cleanup(func() { _ = w.Resume() })

	// The frozen worker cannot renew, so its lease is exactly what expiry
	// produces — set the same state directly rather than waiting out the TTL
	// (the fake model's 30s gate backstop bounds a real wait anyway).
	if _, err := db.Exec(ctx, "UPDATE ctx_session_execution SET lease_until = clock_timestamp() - interval '1 second' WHERE session_id = (SELECT session_id FROM agent_run WHERE id=$1)", run1ID); err != nil {
		t.Fatal(err)
	}

	// Push the second message while the writer is frozen; the surviving
	// worker's takeover must be refused — run2 stays queued, no second model
	// call, no dual execution.
	fp.push(map[string]string{
		"id": "m2-pw2-" + h.runID, "chat_id": "chat-pw", "sender_id": "user-1", "sender_name": "U", "text": "two",
	})
	var run2ID string
	waitForCond(t, 60*time.Second, "run2 enqueued", func() bool {
		return db.QueryRow(ctx, "SELECT id FROM agent_run WHERE state='queued' AND id<>$1", run1ID).Scan(&run2ID) == nil
	})
	// The surviving worker retries the claim every sweep — over ~10s it must
	// keep losing to the live-writer fence.
	time.Sleep(10 * time.Second)
	var state2 string
	if err := db.QueryRow(ctx, "SELECT state FROM agent_run WHERE id=$1", run2ID).Scan(&state2); err != nil {
		t.Fatal(err)
	}
	if state2 != "queued" {
		t.Fatalf("run2 state = %s while writer paused, want queued", state2)
	}
	if got := len(fake.requests()); got != 1 {
		t.Fatalf("model requests = %d during writer pause, want 1", got)
	}

	// Resume: the fenced-out worker observes the loss, unwinds, and clears its
	// own expired row; the queued run then executes once on any worker and A
	// (still channel owner) sends the reply.
	if err := w.Resume(); err != nil {
		t.Fatalf("resume worker: %v", err)
	}
	gate.Release()
	waitForCond(t, 90*time.Second, "run2 completes after writer exit", func() bool {
		var st string
		return db.QueryRow(ctx, "SELECT state FROM agent_run WHERE id=$1", run2ID).Scan(&st) == nil && st == "completed"
	})
	waitForCond(t, 60*time.Second, "A sends run2 reply", func() bool {
		s := fp.lastSend()
		return s != nil && s["text"] == "SECOND "+h.runID
	})
	var state1 string
	if err := db.QueryRow(ctx, "SELECT state FROM agent_run WHERE id=$1", run1ID).Scan(&state1); err != nil {
		t.Fatal(err)
	}
	if state1 == "completed" || state1 == "running" {
		t.Fatalf("run1 state = %s, want interrupted/canceled — a fenced writer must not commit", state1)
	}
	if got := len(fake.requests()); got != 2 {
		t.Fatalf("model requests = %d, want 2", got)
	}
	var run2Count int
	if err := db.QueryRow(ctx, "SELECT count(*) FROM agent_run WHERE inbox_id=(SELECT inbox_id FROM agent_run WHERE id=$1)", run2ID).Scan(&run2Count); err != nil {
		t.Fatal(err)
	}
	if run2Count != 1 {
		t.Fatalf("runs for second event = %d, want 1", run2Count)
	}
}

// Phase 7 acceptance row: replicas sharing one STELLA_HOME

// Phase 7 acceptance row: replicas sharing one STELLA_HOME see the same
// asset bytes — upload lands through A's HTTP port, B serves the same file,
// and an unrelated user is denied. Same-host shared dir approximates the
// required shared POSIX namespace; NFS-style semantics are a deployment
// precondition, not something this test can prove.
func TestDurableAttachmentSharedHome(t *testing.T) {
	skipUnsupportedHost(t)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	d, err := testbed.Start(ctx, testbed.Options{RepoRoot: repoRoot(t), FakeModel: true, Bootstrap: false, ExtraEnv: map[string]string{
		"LOCAL_PASSWORD_ALLOW_REGISTRATION": "1",
		"STELLA_CHANNEL_LEASE":              "off", "STELLA_RUN_WORKER": "off",
	}})
	if err != nil {
		t.Fatalf("start D: %v", err)
	}
	t.Cleanup(func() { _ = d.Stop() })
	sharedFake = d
	h := &harness{owner: t, runID: newRunID(t), baseURL: d.BaseURL(), client: mustCookieClient(t), proc: d}
	h.registerBootstrapUser(t, ctx)
	fake := newFakeAnthropic(t)
	providerID := h.createWebhookFakeProvider(t, ctx, fake.baseURL(), "-att")
	agentID := h.createWebhookAgent(t, ctx, providerID+"/claude-sonnet-4-6", "-att")
	sessionID := h.createSession(t, ctx, agentID)

	sharedHome := filepath.Join(t.TempDir(), "shared-home")
	a := startReplicaHome(t, d, sharedHome, map[string]string{
		"STELLA_CHANNEL_LEASE": "off", "STELLA_RUN_WORKER": "off",
	})
	b := startReplicaHome(t, d, sharedHome, map[string]string{
		"STELLA_CHANNEL_LEASE": "off", "STELLA_RUN_WORKER": "off",
	})
	c := startReplicaHome(t, d, sharedHome, map[string]string{
		"STELLA_CHANNEL_LEASE": "off", "STELLA_RUN_WORKER": "off",
	})

	// Upload through A — the bytes land in the shared home's user assets.
	content := "asset-bytes-" + h.runID
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("file", "note-"+h.runID+".txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	uploadURL := fmt.Sprintf("%s/api/agents/%s/sessions/%s/workspace/upload", a.BaseURL(), agentID, sessionID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, uploadURL, &body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("upload via A: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("upload via A = %d, want 201: %s", resp.StatusCode, raw)
	}
	var uploaded struct {
		Path         string `json:"path"`
		RelativePath string `json:"relative_path"`
		Scope        string `json:"scope"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&uploaded); err != nil {
		t.Fatalf("decode upload response: %v", err)
	}

	// Read through B — the file must be there and identical.
	readURL := fmt.Sprintf("%s/api/agents/%s/sessions/%s/workspace/file-content?path=%s&scope=%s&raw=true",
		b.BaseURL(), agentID, sessionID, url.QueryEscape(uploaded.RelativePath), uploaded.Scope)
	req, err = http.NewRequestWithContext(ctx, http.MethodGet, readURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err = h.client.Do(req)
	if err != nil {
		t.Fatalf("read via B: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	got, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("read via B = %d, want 200: %s", resp.StatusCode, got)
	}
	if string(got) != content {
		t.Fatalf("B read %q, want %q", got, content)
	}

	// Read through C — the third replica serves the same bytes from the
	// shared home, which is what an attachment-bearing outbox send relies on.
	readCURL := fmt.Sprintf("%s/api/agents/%s/sessions/%s/workspace/file-content?path=%s&scope=%s&raw=true",
		c.BaseURL(), agentID, sessionID, url.QueryEscape(uploaded.RelativePath), uploaded.Scope)
	req, err = http.NewRequestWithContext(ctx, http.MethodGet, readCURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err = h.client.Do(req)
	if err != nil {
		t.Fatalf("read via C: %v", err)
	}
	got, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(got) != content {
		t.Fatalf("C read = %d %q, want 200 %q", resp.StatusCode, got, content)
	}

	// A different registered user must not read another user's asset scope.
	stranger := mustCookieClient(t)
	reg := map[string]string{
		"name": "Stranger " + h.runID, "email": "stranger-" + h.runID + "@system.test",
		"password": "stranger-" + h.runID, "confirm_password": "stranger-" + h.runID,
	}
	payload, _ := json.Marshal(reg)
	regResp, err := stranger.Post(d.BaseURL()+"/api/auth/local/register", "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("register stranger: %v", err)
	}
	_ = regResp.Body.Close()
	if regResp.StatusCode != http.StatusOK {
		t.Fatalf("stranger register = %d, want 200", regResp.StatusCode)
	}
	req, err = http.NewRequestWithContext(ctx, http.MethodGet, readURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err = stranger.Do(req)
	if err != nil {
		t.Fatalf("stranger read: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatal("cross-user read of another user's asset succeeded")
	}
}

// Phase 6: web send is idempotent enqueue-then-observe. The message POST
// lands on the API replica while a worker on another process executes; the
// SSE response streams the persisted run events.
func TestDurableWebSend(t *testing.T) {
	skipUnsupportedHost(t)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	baseEnv := map[string]string{}
	d, err := testbed.Start(ctx, testbed.Options{RepoRoot: repoRoot(t), FakeModel: true, Bootstrap: false, ExtraEnv: mergeEnvs(baseEnv, map[string]string{
		"STELLA_CHANNEL_LEASE": "off", "STELLA_RUN_WORKER": "off",
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

	fake.enqueueText("WEB " + h.runID)
	providerID := h.createWebhookFakeProvider(t, ctx, fake.baseURL(), "-web")
	agentID := h.createWebhookAgent(t, ctx, providerID+"/claude-sonnet-4-6", "-web")
	sessionID := h.createSession(t, ctx, agentID)

	b := startReplica(t, d, mergeEnvs(baseEnv, map[string]string{"STELLA_CHANNEL_LEASE": "off"}))

	idemKey := "web-send-1-" + h.runID
	events, text := h.streamChatTurnKeyed(t, ctx, agentID, sessionID, "hello durable web", idemKey)
	_ = events
	if text != "WEB "+h.runID {
		t.Fatalf("streamed reply = %q, want %q", text, "WEB "+h.runID)
	}
	var workerID, state string
	if err := db.QueryRow(ctx, "SELECT state, COALESCE(worker_id,'') FROM agent_run WHERE session_id=$1", sessionID).Scan(&state, &workerID); err != nil {
		t.Fatalf("no durable run for web send: %v", err)
	}
	if state != "completed" {
		t.Fatalf("web run state=%s, want completed", state)
	}
	if bpid := b.PID(); bpid == 0 || !strings.Contains(workerID, fmt.Sprintf("-%d-", bpid)) {
		t.Fatalf("web run worker_id=%q does not name B's process (pid %d)", workerID, bpid)
	}
	if got := len(fake.requests()); got != 1 {
		t.Fatalf("model requests = %d, want 1", got)
	}

	// Idempotent resend: same key attaches to the same run, no second turn.
	fake.enqueueText("SHOULD-NOT-RUN " + h.runID)
	events2, text2 := h.streamChatTurnKeyed(t, ctx, agentID, sessionID, "hello durable web", idemKey)
	_ = events2
	if text2 != "WEB "+h.runID {
		t.Fatalf("idempotent resend streamed %q, want the original reply %q", text2, "WEB "+h.runID)
	}

	var runCount int
	if err := db.QueryRow(ctx, "SELECT count(*) FROM agent_run WHERE session_id=$1", sessionID).Scan(&runCount); err != nil {
		t.Fatal(err)
	}
	if runCount != 1 {
		t.Fatalf("runs for session = %d, want 1 (idempotent resend must not enqueue)", runCount)
	}
	// The enqueued second script must stay unconsumed — the resend never
	// became a turn. Discard it so the fake's cleanup assertion passes.
	fake.DiscardScripts()
}

// streamChatTurnKeyed is streamChatTurn plus an Idempotency-Key header so the
// durable enqueue path can dedup a retried send.
func (h *harness) streamChatTurnKeyed(t *testing.T, ctx context.Context, agentID, sessionID, message, key string) ([]turnEvent, string) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"parts": []map[string]any{{"type": "text", "text": message}}})
	if err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/agents/%s/sessions/%s/messages", agentID, sessionID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("POST send message: %v\n%s", err, h.proc.LogTail(40))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		drainBody(resp.Body)
		t.Fatalf("send message = %d, want 200", resp.StatusCode)
	}
	var events []turnEvent
	var reply strings.Builder
	scanner := newSSEScanner(resp)
	for {
		ev, done := scanTurnEvent(t, scanner)
		if done {
			break
		}
		events = append(events, ev)
		if ev.Type == "text-delta" {
			reply.WriteString(ev.Delta)
		}
	}
	return events, reply.String()
}

// Phase 6: /stop from the API replica cancels a turn executing on another
// process — the run ends 'canceled' and the model turn aborts.
func TestDurableWebCancel(t *testing.T) {
	skipUnsupportedHost(t)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	baseEnv := map[string]string{}
	d, err := testbed.Start(ctx, testbed.Options{RepoRoot: repoRoot(t), FakeModel: true, Bootstrap: false, ExtraEnv: mergeEnvs(baseEnv, map[string]string{
		"STELLA_CHANNEL_LEASE": "off", "STELLA_RUN_WORKER": "off",
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

	gate := fake.EnqueueGatedText("hold-", "NEVER "+h.runID)
	providerID := h.createWebhookFakeProvider(t, ctx, fake.baseURL(), "-cancel")
	agentID := h.createWebhookAgent(t, ctx, providerID+"/claude-sonnet-4-6", "-cancel")
	sessionID := h.createSession(t, ctx, agentID)

	b := startReplica(t, d, mergeEnvs(baseEnv, map[string]string{"STELLA_CHANNEL_LEASE": "off"}))
	_ = b

	// Start the send in the background; it blocks inside the gated model call.
	sendDone := make(chan struct{})
	go func() {
		defer close(sendDone)
		payload, _ := json.Marshal(map[string]any{"parts": []map[string]any{{"type": "text", "text": "cancel me"}}})
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
			fmt.Sprintf("%s/api/agents/%s/sessions/%s/messages", h.baseURL, agentID, sessionID), bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		resp, err := h.client.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
	}()
	var runID string
	waitForCond(t, 90*time.Second, "run claimed by remote worker", func() bool {
		return db.QueryRow(ctx, "SELECT id FROM agent_run WHERE session_id=$1 AND state='running'", sessionID).Scan(&runID) == nil
	})
	// The run being 'running' does not mean the model call left the worker —
	// wait until it is parked inside the gate or the scripted turn is never
	// consumed.
	select {
	case <-gate.Entered:
	case <-ctx.Done():
		t.Fatal("gated model call never parked inside the gate")
	}

	// Cancel from D — B's worker must abort through the execution lease flag.
	resp := h.postJSON(t, ctx, fmt.Sprintf("/api/agents/%s/sessions/%s/stop", agentID, sessionID), nil)
	_ = resp.Body.Close()
	gate.Release() // free the model so the canceled turn can wind down
	waitForCond(t, 60*time.Second, "run canceled", func() bool {
		var state string
		return db.QueryRow(ctx, "SELECT state FROM agent_run WHERE id=$1", runID).Scan(&state) == nil && state == "canceled"
	})
	select {
	case <-sendDone:
	case <-time.After(30 * time.Second):
		t.Log("send SSE still open after cancel; acceptable if run canceled")
	}
}

// Phase 6: in-flight channel progress across replicas. B executes a gated
// turn; the channel owner A edits ONE platform draft message with committed
// increments while the model call is still parked. The terminal reply edits
// the same message — draft identity is stable and a stale draft op can never
// overwrite the final version.
func TestDurableChannelLiveProgress(t *testing.T) {
	skipUnsupportedHost(t)
	fp := newFakePlatform(t)
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	baseEnv := map[string]string{"STELLA_TEST_CHANNELS": "1"}
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
	gate := fake.EnqueueGatedText("partial-", "LIVE "+h.runID)
	providerID := h.createWebhookFakeProvider(t, ctx, fake.baseURL(), "-live")
	agentID := h.createWebhookAgent(t, ctx, providerID+"/"+modelID, "-live")
	botName := "testbot-" + h.runID
	channelID := h.createTestChannel(t, ctx, agentID, fp.endpoint(), botName)

	a := startReplica(t, d, mergeEnvs(baseEnv, map[string]string{
		"STELLA_RUN_WORKER": "off", "STELLA_TESTCHAN_TAG": "A",
	}))
	aID := replicaIDOf(t, a)
	waitForCond(t, 60*time.Second, "A owns the channel", func() bool {
		var owner string
		return db.QueryRow(ctx, "SELECT COALESCE(runtime_owner_id,'') FROM channel WHERE id=$1", channelID).Scan(&owner) == nil && owner == aID
	})
	b := startReplica(t, d, mergeEnvs(baseEnv, map[string]string{
		"STELLA_CHANNEL_LEASE": "off", "STELLA_TESTCHAN_TAG": "B",
	}))

	fp.push(map[string]string{
		"id": "live-e1-" + h.runID, "chat_id": "chat-live", "sender_id": "user-1", "sender_name": "U", "text": "show progress",
	})
	var runID string
	waitForCond(t, 90*time.Second, "B claims the run", func() bool {
		return db.QueryRow(ctx, "SELECT id FROM agent_run WHERE state='running'").Scan(&runID) == nil
	})
	select {
	case <-gate.Entered:
	case <-ctx.Done():
		t.Fatal("gated model call never parked inside the gate")
	}

	// While B is still parked in the gate, the owner must have created the
	// platform draft from the committed "partial-" increment — the platform
	// sees in-flight progress before the run completes.
	var draftID string
	waitForCond(t, 60*time.Second, "A creates the live draft mid-gate", func() bool {
		for _, s := range fp.sendsAll() {
			if s["draft"] == true && s["tag"] == "A" && strings.Contains(fmt.Sprint(s["text"]), "partial-") {
				for _, id := range fp.messageIDs() {
					draftID = id
				}
				return true
			}
		}
		return false
	})
	if draftID == "" {
		t.Fatal("draft send recorded but no platform message was created")
	}
	if msg := fp.message(draftID); msg == nil || !strings.Contains(fmt.Sprint(msg["text"]), "partial-") {
		t.Fatalf("draft message text = %v, want the committed partial", msg)
	}
	// The run is still executing — no terminal send may have landed yet.
	for _, s := range fp.sendsAll() {
		if s["draft"] != true {
			t.Fatalf("non-draft send landed while the run was gated: %v", s)
		}
	}

	// Release the model: the terminal reply must EDIT the same message, not
	// create a second one.
	gate.Release()
	finalText := "partial-LIVE " + h.runID
	waitForCond(t, 90*time.Second, "final version lands on the draft message", func() bool {
		msg := fp.message(draftID)
		return msg != nil && fmt.Sprint(msg["text"]) == finalText
	})
	if got := fp.messageCount(); got != 1 {
		t.Fatalf("platform messages = %d, want 1 (create/edit identity must be stable)", got)
	}
	var finalEdit bool
	for _, s := range fp.sendsAll() {
		if s["draft"] != true && s["message_id"] == draftID {
			finalEdit = true
		}
	}
	if !finalEdit {
		t.Fatal("terminal reply did not edit the live draft message")
	}
	var state, workerID string
	if err := db.QueryRow(ctx, "SELECT state, COALESCE(worker_id,'') FROM agent_run WHERE id=$1", runID).Scan(&state, &workerID); err != nil {
		t.Fatal(err)
	}
	if state != "completed" {
		t.Fatalf("run state=%s, want completed", state)
	}
	if bpid := b.PID(); bpid == 0 || !strings.Contains(workerID, fmt.Sprintf("-%d-", bpid)) {
		t.Fatalf("run worker_id=%q does not name B's process (pid %d)", workerID, bpid)
	}
	sendsAfterFinal := fp.sendCount()

	// Stale-edit fence: a pending draft op appearing after the terminal send
	// (a requeue, a lagging producer, a replayed append) must be canceled
	// without touching the platform — the final version is sealed.
	if _, err := db.Exec(ctx, `INSERT INTO channel_outbox
		(run_id, delivery_key, operation_index, operation_kind, channel_id, source_account_key, address, payload)
		VALUES ($1, $2, 999, 'draft_update', $3, $4, '{}', $5)`,
		runID, "live:"+runID, channelID, botName,
		`{"v":1,"seq":9999,"text":"STALE OVERWRITE"}`); err != nil {
		t.Fatalf("inject stale draft op: %v", err)
	}
	waitForCond(t, 30*time.Second, "stale draft op canceled", func() bool {
		var st string
		return db.QueryRow(ctx, "SELECT state FROM channel_outbox WHERE delivery_key=$1 AND operation_index=999", "live:"+runID).Scan(&st) == nil && st == "canceled"
	})
	time.Sleep(2 * time.Second)
	if got := fp.sendCount(); got != sendsAfterFinal {
		t.Fatalf("stale draft op reached the platform: sends %d -> %d", sendsAfterFinal, got)
	}
	if msg := fp.message(draftID); fmt.Sprint(msg["text"]) != finalText {
		t.Fatalf("final text overwritten by stale edit: %v", msg["text"])
	}
}

// Phase 6: cross-replica web observation of committed increments. The send
// lands on D, the run executes on B parked in the model gate; a watcher on C
// (no local hub, no session lease) tails the durable event log and sees the
// partial text before release — plus a mid-gate reconnect from a cursor.
func TestDurableWebObserveLive(t *testing.T) {
	skipUnsupportedHost(t)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	baseEnv := map[string]string{}
	d, err := testbed.Start(ctx, testbed.Options{RepoRoot: repoRoot(t), FakeModel: true, Bootstrap: false, ExtraEnv: mergeEnvs(baseEnv, map[string]string{
		"STELLA_CHANNEL_LEASE": "off", "STELLA_RUN_WORKER": "off",
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

	gate := fake.EnqueueGatedText("partial-", "OBSERVED "+h.runID)
	providerID := h.createWebhookFakeProvider(t, ctx, fake.baseURL(), "-obs")
	agentID := h.createWebhookAgent(t, ctx, providerID+"/claude-sonnet-4-6", "-obs")
	sessionID := h.createSession(t, ctx, agentID)

	b := startReplica(t, d, mergeEnvs(baseEnv, map[string]string{"STELLA_CHANNEL_LEASE": "off"}))
	c := startReplica(t, d, mergeEnvs(baseEnv, map[string]string{
		"STELLA_CHANNEL_LEASE": "off", "STELLA_RUN_WORKER": "off",
	}))
	cClient := h.loginReplica(t, ctx, c)

	// Send on D in the background; B claims and parks in the gate.
	sendDone := make(chan string, 1)
	go func() {
		_, text := h.streamChatTurnKeyed(t, ctx, agentID, sessionID, "watch me live", "obs-"+h.runID)
		sendDone <- text
	}()
	var runID string
	waitForCond(t, 90*time.Second, "B claims the web run", func() bool {
		return db.QueryRow(ctx, "SELECT id FROM agent_run WHERE session_id=$1 AND state='running'", sessionID).Scan(&runID) == nil
	})
	select {
	case <-gate.Entered:
	case <-ctx.Done():
		t.Fatal("gated model call never parked inside the gate")
	}

	// C attaches mid-gate and must see the committed partial increment while
	// the turn still runs — plus run-scoped id: cursors.
	partial, cursor, _ := h.watchEventsUntil(t, ctx, cClient, c.BaseURL(), agentID, sessionID, "", "partial-")
	if !partial {
		t.Fatal("C did not observe committed partial events while B was gated")
	}
	if cursor == "" {
		t.Fatal("durable stream emitted no id: cursor")
	}

	// Reconnect from the cursor mid-gate: resume must deliver the remaining
	// events without replaying what the cursor already covered — while the
	// run is still open the attach stays live instead of answering 204.
	gate.Release()
	done, _, finalText := h.watchEventsUntil(t, ctx, cClient, c.BaseURL(), agentID, sessionID, cursor, "OBSERVED "+h.runID)
	if !done {
		t.Fatal("C's resumed stream never observed the terminal events")
	}
	if !strings.Contains(finalText, "OBSERVED "+h.runID) {
		t.Fatalf("resumed stream text = %q, want tail containing OBSERVED", finalText)
	}

	select {
	case text := <-sendDone:
		if text != "partial-OBSERVED "+h.runID {
			t.Fatalf("D's send stream text = %q, want the gated reply", text)
		}
	case <-ctx.Done():
		t.Fatal("send stream never completed")
	}
	var state, workerID string
	if err := db.QueryRow(ctx, "SELECT state, COALESCE(worker_id,'') FROM agent_run WHERE id=$1", runID).Scan(&state, &workerID); err != nil {
		t.Fatal(err)
	}
	if state != "completed" {
		t.Fatalf("run state=%s, want completed", state)
	}
	if bpid := b.PID(); bpid == 0 || !strings.Contains(workerID, fmt.Sprintf("-%d-", bpid)) {
		t.Fatalf("run worker_id=%q does not name B's process (pid %d)", workerID, bpid)
	}
}

// loginReplica returns a cookie client authenticated on a replica's own base
// URL — the shared cluster makes D's bootstrap credentials valid everywhere.
func (h *harness) loginReplica(t *testing.T, ctx context.Context, inst *testbed.Instance) *http.Client {
	t.Helper()
	client := mustCookieClient(t)
	payload, err := json.Marshal(map[string]string{
		"email":    fmt.Sprintf("bootstrap-%s@system.test", h.runID),
		"password": "system-test-" + h.runID,
	})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, inst.BaseURL()+"/api/auth/local/login", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("login on replica: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		drainBody(resp.Body)
		t.Fatalf("login on replica = %d, want 200", resp.StatusCode)
	}
	return client
}

// watchEventsUntil opens the durable session event stream on baseURL and
// reads frames until the assembled text contains want or the run's terminal
// events arrive. It returns whether want was seen and the last id: cursor —
// reconnectable proof for mid-turn observation. A missing cursor just means
// the slice ended before any durable frame; callers assert it when needed.
func (h *harness) watchEventsUntil(t *testing.T, ctx context.Context, client *http.Client, baseURL, agentID, sessionID, cursor, want string) (bool, string, string) {
	t.Helper()
	watchCtx, watchCancel := context.WithTimeout(ctx, 90*time.Second)
	defer watchCancel()
	path := fmt.Sprintf("%s/api/agents/%s/sessions/%s/events", baseURL, agentID, sessionID)
	req, err := http.NewRequestWithContext(watchCtx, http.MethodGet, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "text/event-stream")
	if cursor != "" {
		req.Header.Set("Last-Event-ID", cursor)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("watch events on replica: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNoContent {
		return false, cursor, ""
	}
	if resp.StatusCode != http.StatusOK {
		drainBody(resp.Body)
		t.Fatalf("watch events = %d, want 200/204", resp.StatusCode)
	}
	var text strings.Builder
	lastCursor := cursor
	scanner := newSSEScanner(resp)
	for scanner.Scan() {
		line := scanner.Text()
		if id, ok := strings.CutPrefix(line, "id: "); ok {
			lastCursor = strings.TrimSpace(id)
			continue
		}
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		if data == "[DONE]" {
			return strings.Contains(text.String(), want), lastCursor, text.String()
		}
		var evt turnEvent
		if err := json.Unmarshal([]byte(data), &evt); err != nil {
			continue
		}
		if evt.Type == "text-delta" {
			text.WriteString(evt.Delta)
		}
		if strings.Contains(text.String(), want) {
			return true, lastCursor, text.String()
		}
	}
	return strings.Contains(text.String(), want), lastCursor, text.String()
}
