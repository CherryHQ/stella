package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/authz"
	agentaccess "github.com/CherryHQ/stella/internal/core/access"
	"github.com/CherryHQ/stella/internal/core/agenterr"
	"github.com/CherryHQ/stella/internal/observability"
	"github.com/CherryHQ/stella/internal/webhook"
)

// --- fakes for the deep capability-admission interface -----------------------

type fakeIngress struct {
	cand       webhook.Candidate
	resolveErr error
	admit      func(context.Context, webhook.Candidate, webhook.AdmitCallback) error
}

func (f *fakeIngress) ResolveCandidate(context.Context, string) (webhook.Candidate, error) {
	return f.cand, f.resolveErr
}

func (f *fakeIngress) Admit(ctx context.Context, c webhook.Candidate, cb webhook.AdmitCallback) error {
	return f.admit(ctx, c, cb)
}

type fakeRunPort struct{ run *fakeAgentRun }

func (f fakeRunPort) Get(string) webhookAgentRun {
	if f.run == nil {
		return nil
	}
	return f.run
}

type fakeAgentRun struct {
	chatCalls    atomic.Int32
	sessionCalls atomic.Int32
	stream       <-chan agent.Event
	chatErr      error
	lastReq      agent.ChatRequest

	archiveCalls   atomic.Int32
	archiveErr     error
	archivedUserID string
	archivedAgent  string
	archivedID     string
	archivedAuth   authz.Authority
}

func (f *fakeAgentRun) ArchiveSession(_ context.Context, authority authz.Authority, userID, agentID, sessionID string) error {
	f.archiveCalls.Add(1)
	f.archivedAuth = authority
	f.archivedUserID = userID
	f.archivedAgent = agentID
	f.archivedID = sessionID
	return f.archiveErr
}

func (f *fakeAgentRun) ResolvePrivateChannelSession(context.Context, authz.Authority, string, string, string, session.Channel) (session.Info, error) {
	f.sessionCalls.Add(1)
	return session.Info{ID: "sess-1"}, nil
}

func (f *fakeAgentRun) NewSession(context.Context, authz.Authority, string, string, string, session.Kind, session.Channel) (session.Info, error) {
	f.sessionCalls.Add(1)
	return session.Info{ID: "sess-1"}, nil
}

func (f *fakeAgentRun) ChatAdmitted(_ context.Context, req agent.ChatRequest) (<-chan agent.Event, error) {
	f.chatCalls.Add(1)
	f.lastReq = req
	if f.chatErr != nil {
		return nil, f.chatErr
	}
	return f.stream, nil
}

func newIngressServer(t *testing.T, ingress webhookIngressPort, run *fakeAgentRun) *Server {
	t.Helper()
	return newIngressServerConfig(t, ingress, run)
}

func newIngressServerConfig(t *testing.T, ingress webhookIngressPort, run *fakeAgentRun) *Server {
	t.Helper()
	return &Server{
		log:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		webhookLimiter: newWebhookLimiter(100, 100),
		webhookIngress: ingress,
		webhookRun:     fakeRunPort{run: run},
		runtimeCtx:     context.Background(),
	}
}

func capabilityRequest(t *testing.T, wait *bool) *http.Request {
	t.Helper()
	return capabilityRequestWithMode(t, wait, "ephemeral", strings.NewReader("hello world"))
}

func capabilityRequestBody(t *testing.T, wait *bool, body io.Reader) *http.Request {
	t.Helper()
	return capabilityRequestWithMode(t, wait, "ephemeral", body)
}

func capabilityRequestWithMode(t *testing.T, wait *bool, sessionMode string, body io.Reader) *http.Request {
	t.Helper()
	options := webhookInvocationOptions{wait: false, sessionMode: sessionMode}
	if wait != nil {
		options.wait = *wait
	}
	ctx := context.WithValue(context.Background(), webhookCapabilityCtxKey{}, "stella_whk_capability")
	ctx = context.WithValue(ctx, webhookInvocationCtxKey{}, options)
	req := httptest.NewRequest(http.MethodPost, sanitizedWebhookPath, body).WithContext(ctx)
	// A caller-supplied Authorization header must be ignored: identity comes only
	// from the capability.
	req.Header.Set("Authorization", "Bearer stella_pat_should_be_ignored")
	return req
}

func closedStream(events ...agent.Event) <-chan agent.Event {
	ch := make(chan agent.Event, len(events)+1)
	for _, e := range events {
		ch <- e
	}
	close(ch)
	return ch
}

// TestWebhookIngressAdmitsFixedOwnerOnce proves a fixed-owner generic POST
// reaches exactly one ChatAdmitted with the endpoint's fixed authority — never a
// caller-selected one — and no Authorization header influences it.
func TestWebhookIngressAdmitsFixedOwnerOnce(t *testing.T) {
	authority, err := agentaccess.WorkerAgentAuthority("owner-1", "agentA")
	if err != nil {
		t.Fatalf("WorkerAgentAuthority: %v", err)
	}
	run := &fakeAgentRun{stream: closedStream(agent.Event{Text: "ok"})}
	ingress := &fakeIngress{
		cand: webhook.Candidate{WebhookID: "c"},
		admit: func(ctx context.Context, _ webhook.Candidate, cb webhook.AdmitCallback) error {
			return cb(ctx, webhook.AdmittedInvocation{WebhookID: "c", UserID: "owner-1", AgentID: "agentA", Provider: webhook.ProviderGeneric, Authority: authority, WaitTimeoutSeconds: 60, MaxRunTimeoutSeconds: 300})
		},
	}
	s := newIngressServer(t, ingress, run)

	async := false
	rr := httptest.NewRecorder()
	s.handleWebhookIngress(rr, capabilityRequest(t, &async))
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body: %s)", rr.Code, rr.Body.String())
	}
	if got := run.chatCalls.Load(); got != 1 {
		t.Fatalf("ChatAdmitted calls = %d, want 1", got)
	}
	if run.lastReq.Authority != authority {
		t.Fatalf("run authority = %+v, want fixed worker authority %+v", run.lastReq.Authority, authority)
	}
	if run.lastReq.UserID != "owner-1" || run.lastReq.AgentID != "agentA" {
		t.Fatalf("run identity = %s/%s, want owner-1/agentA", run.lastReq.UserID, run.lastReq.AgentID)
	}
}

func TestWebhookIngressWaitTrueReturnsOutput(t *testing.T) {
	authority, _ := agentaccess.WorkerAgentAuthority("owner-1", "agentA")
	run := &fakeAgentRun{stream: closedStream(agent.Event{Text: "hello reply"})}
	ingress := &fakeIngress{cand: webhook.Candidate{WebhookID: "c"}, admit: func(ctx context.Context, _ webhook.Candidate, cb webhook.AdmitCallback) error {
		return cb(ctx, webhook.AdmittedInvocation{WebhookID: "c", UserID: "owner-1", AgentID: "agentA", Authority: authority, WaitTimeoutSeconds: 60, MaxRunTimeoutSeconds: 300})
	}}
	s := newIngressServer(t, ingress, run)

	wait := true
	rr := httptest.NewRecorder()
	s.handleWebhookIngress(rr, capabilityRequest(t, &wait))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "hello reply") {
		t.Fatalf("body = %s, want the agent output", rr.Body.String())
	}
}

func TestWebhookIngressWaitDisconnectReturnsAndFinishesRunInBackground(t *testing.T) {
	authority, err := agentaccess.WorkerAgentAuthority("owner-1", "agentA")
	if err != nil {
		t.Fatal(err)
	}
	stream := make(chan agent.Event)
	run := &fakeAgentRun{stream: stream}
	ingress := &fakeIngress{
		cand: webhook.Candidate{WebhookID: "disconnect"},
		admit: func(ctx context.Context, _ webhook.Candidate, cb webhook.AdmitCallback) error {
			return cb(ctx, webhook.AdmittedInvocation{WebhookID: "disconnect", UserID: "owner-1", AgentID: "agentA", Provider: webhook.ProviderGeneric, Authority: authority, WaitTimeoutSeconds: 600, MaxRunTimeoutSeconds: 300})
		},
	}
	s := newIngressServer(t, ingress, run)
	wait := true
	req := capabilityRequest(t, &wait)
	ctx, cancel := context.WithCancel(req.Context())
	req = req.WithContext(ctx)
	done := make(chan struct{})
	go func() {
		s.handleWebhookIngress(httptest.NewRecorder(), req)
		close(done)
	}()
	deadline := time.Now().Add(time.Second)
	for run.chatCalls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if run.chatCalls.Load() != 1 {
		t.Fatal("run was not admitted")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("handler remained blocked after client disconnect")
	}
	close(stream)
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		s.webhookLimiter.mu.Lock()
		inflight := s.webhookLimiter.inflight["disconnect"]
		s.webhookLimiter.mu.Unlock()
		if inflight == 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("background drainer did not release run slot")
}

func TestWebhookIngressUnknownCapabilityIs404(t *testing.T) {
	run := &fakeAgentRun{}
	ingress := &fakeIngress{resolveErr: webhook.ErrNotFound, admit: func(context.Context, webhook.Candidate, webhook.AdmitCallback) error {
		t.Fatal("Admit must not run when the candidate does not resolve")
		return nil
	}}
	s := newIngressServer(t, ingress, run)
	rr := httptest.NewRecorder()
	s.handleWebhookIngress(rr, capabilityRequest(t, nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body: %s)", rr.Code, rr.Body.String())
	}
	if run.chatCalls.Load() != 0 {
		t.Fatal("no ChatAdmitted may occur for an unknown capability")
	}
}

// TestWebhookIngressAdmitFailuresAreOpaque404 proves that a credential that
// stops resolving during admission (rotate/revoke) and a withdrawn owner
// permission both fail closed with an opaque 404 and never start a run.
func TestWebhookIngressAdmitFailuresAreOpaque404(t *testing.T) {
	for _, admitErr := range []error{webhook.ErrNotFound, webhook.ErrUserAgentForbidden} {
		t.Run(admitErr.Error(), func(t *testing.T) {
			run := &fakeAgentRun{}
			ingress := &fakeIngress{cand: webhook.Candidate{WebhookID: "c"}, admit: func(context.Context, webhook.Candidate, webhook.AdmitCallback) error {
				return admitErr
			}}
			s := newIngressServer(t, ingress, run)
			rr := httptest.NewRecorder()
			s.handleWebhookIngress(rr, capabilityRequest(t, nil))
			if rr.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404", rr.Code)
			}
			if run.chatCalls.Load() != 0 {
				t.Fatal("no run may start on a failed admission")
			}
		})
	}
}

func TestWebhookIngressBusyIs429(t *testing.T) {
	authority, _ := agentaccess.WorkerAgentAuthority("owner-1", "agentA")
	run := &fakeAgentRun{chatErr: fmt.Errorf("%w: session s1", agenterr.ErrSessionBusy)}
	ingress := &fakeIngress{cand: webhook.Candidate{WebhookID: "c"}, admit: func(ctx context.Context, _ webhook.Candidate, cb webhook.AdmitCallback) error {
		return cb(ctx, webhook.AdmittedInvocation{WebhookID: "c", UserID: "owner-1", AgentID: "agentA", Authority: authority, WaitTimeoutSeconds: 60, MaxRunTimeoutSeconds: 300})
	}}
	s := newIngressServer(t, ingress, run)
	rr := httptest.NewRecorder()
	s.handleWebhookIngress(rr, capabilityRequest(t, nil))
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 (body: %s)", rr.Code, rr.Body.String())
	}
}

func TestWebhookIngressUnavailableWhenPortNil(t *testing.T) {
	s := &Server{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	rr := httptest.NewRecorder()
	s.handleWebhookIngress(rr, capabilityRequest(t, nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rr.Code)
	}
}

// TestWebhookIngressInflightCapReservesBeforeSession proves the in-flight run
// slot is reserved before any session work: an at-capacity rejection creates or
// resolves no session, starts no run, returns 429, and leaks no slot.
func TestWebhookIngressInflightCapReservesBeforeSession(t *testing.T) {
	authority, _ := agentaccess.WorkerAgentAuthority("owner-1", "agentA")
	run := &fakeAgentRun{}
	ingress := &fakeIngress{cand: webhook.Candidate{WebhookID: "c"}, admit: func(ctx context.Context, _ webhook.Candidate, cb webhook.AdmitCallback) error {
		return cb(ctx, webhook.AdmittedInvocation{WebhookID: "c", UserID: "owner-1", AgentID: "agentA", Authority: authority, WaitTimeoutSeconds: 60, MaxRunTimeoutSeconds: 300})
	}}
	s := newIngressServer(t, ingress, run)

	// Saturate the per-endpoint in-flight cap so the handler's reservation fails.
	for range defaultWebhookMaxInflight {
		if !s.webhookLimiter.beginRun("c") {
			t.Fatal("setup: beginRun should succeed until the cap")
		}
	}

	rr := httptest.NewRecorder()
	s.handleWebhookIngress(rr, capabilityRequest(t, nil))
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 (body: %s)", rr.Code, rr.Body.String())
	}
	if run.sessionCalls.Load() != 0 {
		t.Fatal("a session was created/resolved despite the in-flight-cap rejection")
	}
	if run.chatCalls.Load() != 0 {
		t.Fatal("a run started despite the in-flight-cap rejection")
	}
	// No slot leaked: the count is exactly what the test reserved — the rejected
	// request neither reserved nor released a slot.
	if got := s.webhookLimiter.inflight["c"]; got != defaultWebhookMaxInflight {
		t.Fatalf("inflight = %d, want %d (slot leaked)", got, defaultWebhookMaxInflight)
	}
}

// TestWebhookIngressBusyAuditModeMatchesRequest proves a busy rejection is logged
// with the mode the caller actually requested: async for a fire-and-forget
// request, sync for a wait request.
func TestWebhookIngressBusyAuditModeMatchesRequest(t *testing.T) {
	authority, _ := agentaccess.WorkerAgentAuthority("owner-1", "agentA")
	cases := []struct {
		name     string
		wait     bool
		wantMode string
	}{
		{"async busy", false, "mode=async"},
		{"sync busy", true, "mode=sync"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			run := &fakeAgentRun{chatErr: fmt.Errorf("%w: session s1", agenterr.ErrSessionBusy)}
			ingress := &fakeIngress{cand: webhook.Candidate{WebhookID: "c"}, admit: func(ctx context.Context, _ webhook.Candidate, cb webhook.AdmitCallback) error {
				return cb(ctx, webhook.AdmittedInvocation{WebhookID: "c", UserID: "owner-1", AgentID: "agentA", Authority: authority, WaitTimeoutSeconds: 60, MaxRunTimeoutSeconds: 300})
			}}
			s := newIngressServer(t, ingress, run)
			s.log = slog.New(slog.NewTextHandler(&buf, nil))

			wait := tc.wait
			rr := httptest.NewRecorder()
			s.handleWebhookIngress(rr, capabilityRequest(t, &wait))
			if rr.Code != http.StatusTooManyRequests {
				t.Fatalf("status = %d, want 429", rr.Code)
			}
			out := buf.String()
			if !strings.Contains(out, "status=busy") || !strings.Contains(out, tc.wantMode) {
				t.Fatalf("audit log = %q, want status=busy and %s", out, tc.wantMode)
			}
		})
	}
}

// TestWebhookIngressArchivesEphemeralSessionOnAdmitFailure proves the
// pre-admission compensation: when a fresh ephemeral session is created but
// ChatAdmitted then fails, that exact session is archived once with the fixed
// identity, and the original admission error is preserved.
func TestWebhookIngressArchivesEphemeralSessionOnAdmitFailure(t *testing.T) {
	authority, _ := agentaccess.WorkerAgentAuthority("owner-1", "agentA")
	run := &fakeAgentRun{chatErr: errors.New("runtime unavailable")}
	ingress := &fakeIngress{cand: webhook.Candidate{WebhookID: "c"}, admit: func(ctx context.Context, _ webhook.Candidate, cb webhook.AdmitCallback) error {
		return cb(ctx, webhook.AdmittedInvocation{WebhookID: "c", UserID: "owner-1", AgentID: "agentA", Authority: authority, WaitTimeoutSeconds: 60, MaxRunTimeoutSeconds: 300})
	}}
	s := newIngressServer(t, ingress, run) // ephemeral (default session mode)

	rr := httptest.NewRecorder()
	s.handleWebhookIngress(rr, capabilityRequest(t, nil))

	if run.sessionCalls.Load() != 1 || run.chatCalls.Load() != 1 {
		t.Fatalf("sessionCalls=%d chatCalls=%d, want 1/1", run.sessionCalls.Load(), run.chatCalls.Load())
	}
	if got := run.archiveCalls.Load(); got != 1 {
		t.Fatalf("ArchiveSession calls = %d, want exactly 1", got)
	}
	if run.archivedID != "sess-1" || run.archivedUserID != "owner-1" || run.archivedAgent != "agentA" {
		t.Fatalf("archived identity = %s/%s/%s, want sess-1/owner-1/agentA", run.archivedID, run.archivedUserID, run.archivedAgent)
	}
	if run.archivedAuth != authority {
		t.Fatalf("archived authority = %+v, want the fixed worker authority", run.archivedAuth)
	}
	// The original admission error is preserved in the response, not masked by cleanup.
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (original admission failure)", rr.Code)
	}
}

// TestWebhookIngressDoesNotArchivePersistentSession proves a pre-existing
// persistent session is never archived when admission fails.
func TestWebhookIngressDoesNotArchivePersistentSession(t *testing.T) {
	authority, _ := agentaccess.WorkerAgentAuthority("owner-1", "agentA")
	run := &fakeAgentRun{chatErr: fmt.Errorf("%w: session s1", agenterr.ErrSessionBusy)}
	ingress := &fakeIngress{cand: webhook.Candidate{WebhookID: "c"}, admit: func(ctx context.Context, _ webhook.Candidate, cb webhook.AdmitCallback) error {
		return cb(ctx, webhook.AdmittedInvocation{WebhookID: "c", UserID: "owner-1", AgentID: "agentA", Authority: authority, WaitTimeoutSeconds: 60, MaxRunTimeoutSeconds: 300})
	}}
	s := newIngressServer(t, ingress, run)

	rr := httptest.NewRecorder()
	s.handleWebhookIngress(rr, capabilityRequestWithMode(t, nil, "persistent", strings.NewReader("hello world")))

	if run.sessionCalls.Load() != 1 {
		t.Fatalf("sessionCalls = %d, want 1 (persistent resolve)", run.sessionCalls.Load())
	}
	if got := run.archiveCalls.Load(); got != 0 {
		t.Fatalf("ArchiveSession calls = %d, want 0 for a persistent session", got)
	}
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 (busy)", rr.Code)
	}
}

// TestWebhookIngressArchiveFailureIsLoggedNotMasked proves a cleanup failure is
// logged without changing the original admission outcome.
func TestWebhookIngressArchiveFailureIsLoggedNotMasked(t *testing.T) {
	authority, _ := agentaccess.WorkerAgentAuthority("owner-1", "agentA")
	run := &fakeAgentRun{chatErr: errors.New("runtime unavailable"), archiveErr: errors.New("archive boom")}
	ingress := &fakeIngress{cand: webhook.Candidate{WebhookID: "c"}, admit: func(ctx context.Context, _ webhook.Candidate, cb webhook.AdmitCallback) error {
		return cb(ctx, webhook.AdmittedInvocation{WebhookID: "c", UserID: "owner-1", AgentID: "agentA", Authority: authority, WaitTimeoutSeconds: 60, MaxRunTimeoutSeconds: 300})
	}}
	var buf bytes.Buffer
	s := newIngressServer(t, ingress, run)
	s.log = slog.New(slog.NewTextHandler(&buf, nil))

	rr := httptest.NewRecorder()
	s.handleWebhookIngress(rr, capabilityRequest(t, nil))

	if run.archiveCalls.Load() != 1 {
		t.Fatalf("ArchiveSession calls = %d, want 1", run.archiveCalls.Load())
	}
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (original error preserved)", rr.Code)
	}
	if out := buf.String(); !strings.Contains(out, "archive orphaned webhook session") {
		t.Fatalf("cleanup failure not logged: %q", out)
	}
	// The capability plaintext must not appear in the cleanup log.
	if strings.Contains(buf.String(), "stella_whk_") {
		t.Fatalf("capability leaked into cleanup log: %q", buf.String())
	}
}

func boolPtr(b bool) *bool { return &b }

// TestWebhookReadDeadlineEnforcedByRealServer drives the exact production
// readWebhookBody through the production observability + access-log chain on a
// real connection, with a short timeout: a stalled body must time out and be
// classified by isWebhookReadTimeout. It calls the production function (no
// algorithm duplicated in the test) to avoid false confidence.
func TestWebhookReadDeadlineEnforcedByRealServer(t *testing.T) {
	s := &Server{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	resCh := make(chan error, 1)
	h := observability.Handler(s.accessLogMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := s.readWebhookBody(w, r, 150*time.Millisecond)
		resCh <- err
		w.WriteHeader(http.StatusOK)
	})))
	srv := httptest.NewServer(h)
	defer srv.Close()

	pr, pw := io.Pipe()
	defer func() { _ = pw.Close() }()
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/webhooks/x", pr)
	if err != nil {
		t.Fatal(err)
	}
	req.ContentLength = 1024 // promise bytes the pipe never delivers → the server read stalls
	go func() {
		if resp, derr := http.DefaultClient.Do(req); derr == nil {
			_ = resp.Body.Close()
		}
	}()

	select {
	case err := <-resCh:
		if !isWebhookReadTimeout(err) {
			t.Fatalf("production readWebhookBody through the real chain did not time out: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("readWebhookBody never observed the read-deadline timeout")
	}
}

// deadlineRecorderWriter is a ResponseWriter whose ResponseController supports
// SetReadDeadline (like a real connection) and records every deadline set, so a
// test can assert readWebhookBody sets a bounded deadline then clears it.
type deadlineRecorderWriter struct {
	http.ResponseWriter
	deadlines []time.Time
}

func (w *deadlineRecorderWriter) SetReadDeadline(t time.Time) error {
	w.deadlines = append(w.deadlines, t)
	return nil
}

// TestReadWebhookBodySetsThenClearsDeadline proves the production readWebhookBody
// sets a nonzero read deadline before the read and clears it to zero afterward,
// via a supported ResponseController fake.
func TestReadWebhookBodySetsThenClearsDeadline(t *testing.T) {
	s := &Server{}
	fw := &deadlineRecorderWriter{ResponseWriter: httptest.NewRecorder()}
	req := httptest.NewRequest(http.MethodPost, sanitizedWebhookPath, strings.NewReader("hello"))

	body, err := s.readWebhookBody(fw, req, 7*time.Second)
	if err != nil {
		t.Fatalf("readWebhookBody: %v", err)
	}
	if string(body) != "hello" {
		t.Fatalf("body = %q, want hello", string(body))
	}
	if len(fw.deadlines) != 2 {
		t.Fatalf("expected set-then-clear (2 deadline calls), got %d", len(fw.deadlines))
	}
	if fw.deadlines[0].IsZero() {
		t.Fatal("first deadline must be nonzero (a bounded read deadline)")
	}
	if !fw.deadlines[1].IsZero() {
		t.Fatal("second deadline must be zero (cleared immediately after the read)")
	}
}

// admitAll is a fakeIngress whose Admit always invokes the callback with a fixed
// invocation, counting how many times it did so.
func admitAll(t *testing.T, calls *atomic.Int32) *fakeIngress {
	t.Helper()
	authority, _ := agentaccess.WorkerAgentAuthority("owner-1", "agentA")
	return &fakeIngress{cand: webhook.Candidate{WebhookID: "c"}, admit: func(ctx context.Context, _ webhook.Candidate, cb webhook.AdmitCallback) error {
		if calls != nil {
			calls.Add(1)
		}
		return cb(ctx, webhook.AdmittedInvocation{WebhookID: "c", UserID: "owner-1", AgentID: "agentA", Authority: authority, WaitTimeoutSeconds: 60, MaxRunTimeoutSeconds: 300})
	}}
}

// limiterCount reads a limiter counter under its mutex, safe to poll while a
// request goroutine mutates it.
func limiterCount(l *webhookLimiter, which, key string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	if which == "ingress" {
		return l.ingress[key]
	}
	return l.inflight[key]
}

func waitForCond(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition not met within deadline")
}

// TestWebhookIngressReleasesSlotBeforeSyncWait proves the ingress slot covers
// only body/admission: while a first sync (wait=true) request is still
// waiting on its admitted run's reply, its ingress slot is already released, so a
// second request acquires ingress capacity — while the first request's run slot
// remains held (run accounting stays active).
func TestWebhookIngressReleasesSlotBeforeSyncWait(t *testing.T) {
	authority, _ := agentaccess.WorkerAgentAuthority("owner-1", "agentA")
	stream := make(chan agent.Event) // open and never yields → request 1 blocks in the sync wait
	run := &fakeAgentRun{stream: stream}
	ingress := &fakeIngress{cand: webhook.Candidate{WebhookID: "c"}, admit: func(ctx context.Context, _ webhook.Candidate, cb webhook.AdmitCallback) error {
		return cb(ctx, webhook.AdmittedInvocation{WebhookID: "c", UserID: "owner-1", AgentID: "agentA", Authority: authority, WaitTimeoutSeconds: 60, MaxRunTimeoutSeconds: 300})
	}}
	s := newIngressServer(t, ingress, run)
	s.webhookLimiter.maxIngress = 1 // only one ingress slot at a time

	// Request 1 is synchronous: it admits (run slot held), releases ingress, then
	// blocks waiting for the reply.
	rr1 := httptest.NewRecorder()
	done1 := make(chan struct{})
	go func() {
		s.handleWebhookIngress(rr1, capabilityRequest(t, boolPtr(true)))
		close(done1)
	}()

	// Once request 1 has admitted and entered its wait, its run slot is held and
	// its ingress slot is free again.
	waitForCond(t, func() bool {
		return limiterCount(s.webhookLimiter, "inflight", "c") == 1 &&
			limiterCount(s.webhookLimiter, "ingress", "c") == 0
	})

	// Request 2 can acquire the single ingress slot even though run 1 is still
	// waiting — proving the slot did not cover the sync wait.
	rr2 := httptest.NewRecorder()
	s.handleWebhookIngress(rr2, capabilityRequest(t, boolPtr(false)))
	if rr2.Code != http.StatusAccepted {
		t.Fatalf("second request = %d, want 202 (ingress capacity must be free during a sync wait)", rr2.Code)
	}
	// Run-slot accounting stayed active: run 1 still held, plus run 2.
	if got := limiterCount(s.webhookLimiter, "inflight", "c"); got != 2 {
		t.Fatalf("inflight run slots = %d, want 2 (run 1 held through its wait, run 2 admitted)", got)
	}

	close(stream) // let both runs drain
	<-done1
}

// TestWebhookIngressSlotGatesBeforeBodyAndRecovers proves the per-endpoint
// ingress slot gates before any body read and admission: an at-capacity request
// fails promptly without consuming an acceptance token, run slot, session, or
// admission, and the slot recovers once released.
func TestWebhookIngressSlotGatesBeforeBodyAndRecovers(t *testing.T) {
	var admitCalls atomic.Int32
	run := &fakeAgentRun{stream: closedStream()}
	s := newIngressServer(t, admitAll(t, &admitCalls), run)
	s.webhookLimiter.maxIngress = 1

	// Saturate the single ingress slot, then a fresh request hits the ceiling.
	if !s.webhookLimiter.acquireIngress("c") {
		t.Fatal("setup: first ingress slot should be free")
	}
	rr := httptest.NewRecorder()
	s.handleWebhookIngress(rr, capabilityRequest(t, nil))
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 (body: %s)", rr.Code, rr.Body.String())
	}
	if run.sessionCalls.Load() != 0 || run.chatCalls.Load() != 0 || admitCalls.Load() != 0 {
		t.Fatal("no body/admit/session work may happen once the ingress slot is exhausted")
	}
	if s.webhookLimiter.buckets["c"] != nil {
		t.Fatal("an ingress-gated request must not consume an acceptance token")
	}
	if s.webhookLimiter.inflight["c"] != 0 {
		t.Fatal("an ingress-gated request must not consume a run slot")
	}

	// Release the held slot; the next request now admits.
	s.webhookLimiter.releaseIngress("c")
	rr2 := httptest.NewRecorder()
	s.handleWebhookIngress(rr2, capabilityRequest(t, boolPtr(false)))
	if rr2.Code != http.StatusAccepted {
		t.Fatalf("recovered status = %d, want 202 (body: %s)", rr2.Code, rr2.Body.String())
	}
	if s.webhookLimiter.ingress["c"] != 0 {
		t.Fatalf("ingress slot leaked after completion: %d", s.webhookLimiter.ingress["c"])
	}
}

// signalBody blocks the first Read until released, modeling a slow client body
// that holds the ingress slot.
type signalBody struct {
	firstRead chan struct{}
	release   chan struct{}
	sent      bool
}

func (b *signalBody) Read(p []byte) (int, error) {
	if !b.sent {
		close(b.firstRead)
		<-b.release
		b.sent = true
		return copy(p, "trigger"), nil
	}
	return 0, io.EOF
}

func (b *signalBody) Close() error { return nil }

// TestWebhookIngressSlowBodyHoldsSlotThenRecovers proves a slow body holds the
// ingress slot for its whole read: a concurrent request to the same endpoint is
// rejected promptly, and the slot recovers on normal completion.
func TestWebhookIngressSlowBodyHoldsSlotThenRecovers(t *testing.T) {
	var admitCalls atomic.Int32
	run := &fakeAgentRun{stream: closedStream()}
	s := newIngressServer(t, admitAll(t, &admitCalls), run)
	s.webhookLimiter.maxIngress = 1

	sb := &signalBody{firstRead: make(chan struct{}), release: make(chan struct{})}
	rr1 := httptest.NewRecorder()
	done1 := make(chan struct{})
	go func() {
		s.handleWebhookIngress(rr1, capabilityRequestBody(t, boolPtr(false), sb))
		close(done1)
	}()
	<-sb.firstRead // request 1 is now inside the body read, holding the slot.

	// A second request to the same endpoint is rejected while the slot is held.
	rr2 := httptest.NewRecorder()
	s.handleWebhookIngress(rr2, capabilityRequest(t, nil))
	if rr2.Code != http.StatusTooManyRequests {
		t.Fatalf("concurrent status = %d, want 429", rr2.Code)
	}
	if admitCalls.Load() != 0 {
		t.Fatal("the slow-body request must not have admitted yet")
	}

	// Release the slow body; request 1 completes and the slot recovers.
	close(sb.release)
	<-done1
	if rr1.Code != http.StatusAccepted {
		t.Fatalf("slow-body status = %d, want 202 (body: %s)", rr1.Code, rr1.Body.String())
	}
	if s.webhookLimiter.ingress["c"] != 0 {
		t.Fatalf("ingress slot not recovered after completion: %d", s.webhookLimiter.ingress["c"])
	}
}

// TestWebhookIngressOversizedBodyIs413 proves a body over 256 KiB is rejected
// with 413 before any acceptance token, session, or run, and never leaks the
// capability.
func TestWebhookIngressOversizedBodyIs413(t *testing.T) {
	var admitCalls atomic.Int32
	run := &fakeAgentRun{stream: closedStream()}
	s := newIngressServer(t, admitAll(t, &admitCalls), run)

	big := bytes.NewReader(bytes.Repeat([]byte("a"), maxWebhookBody+1))
	rr := httptest.NewRecorder()
	s.handleWebhookIngress(rr, capabilityRequestBody(t, nil, big))
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413 (body: %s)", rr.Code, rr.Body.String())
	}
	if admitCalls.Load() != 0 || run.sessionCalls.Load() != 0 || run.chatCalls.Load() != 0 {
		t.Fatal("oversized body must not start admission/session/run")
	}
	if s.webhookLimiter.buckets["c"] != nil {
		t.Fatal("oversized body must not consume an acceptance token")
	}
	if strings.Contains(rr.Body.String(), "stella_whk_") {
		t.Fatalf("capability leaked in 413 response: %s", rr.Body.String())
	}
	if s.webhookLimiter.ingress["c"] != 0 {
		t.Fatal("ingress slot leaked on the 413 path")
	}
}

type deadlineBody struct{}

func (deadlineBody) Read([]byte) (int, error) { return 0, os.ErrDeadlineExceeded }
func (deadlineBody) Close() error             { return nil }

// TestWebhookIngressStalledReadTimesOut proves a stalled read returns a bounded
// client error (408) with no admission/session/run and no capability leak.
func TestWebhookIngressStalledReadTimesOut(t *testing.T) {
	var admitCalls atomic.Int32
	run := &fakeAgentRun{stream: closedStream()}
	s := newIngressServer(t, admitAll(t, &admitCalls), run)

	rr := httptest.NewRecorder()
	s.handleWebhookIngress(rr, capabilityRequestBody(t, nil, deadlineBody{}))
	if rr.Code != http.StatusRequestTimeout {
		t.Fatalf("status = %d, want 408 (body: %s)", rr.Code, rr.Body.String())
	}
	if admitCalls.Load() != 0 || run.sessionCalls.Load() != 0 || run.chatCalls.Load() != 0 {
		t.Fatal("a stalled read must not start admission/session/run")
	}
	if s.webhookLimiter.buckets["c"] != nil {
		t.Fatal("a stalled read must not consume an acceptance token")
	}
	if strings.Contains(rr.Body.String(), "stella_whk_") {
		t.Fatalf("capability leaked in 408 response: %s", rr.Body.String())
	}
	if s.webhookLimiter.ingress["c"] != 0 {
		t.Fatal("ingress slot leaked on the timeout path")
	}
}

func TestDrainWebhookStream(t *testing.T) {
	res := <-drainWebhookStream(closedStream(agent.Event{Text: "Hello, "}, agent.Event{Text: "world"}, agent.Event{Reasoning: "ignored"}), true)
	if res.err != nil {
		t.Fatalf("unexpected err: %v", res.err)
	}
	if res.output != "Hello, world" {
		t.Fatalf("output = %q, want %q", res.output, "Hello, world")
	}
}

// TestDrainWebhookStreamPreservesBusy pins the error identity the busy branch
// depends on: a wrapped ErrSessionBusy must survive the drain.
func TestDrainWebhookStreamBoundsOrDiscardsOutput(t *testing.T) {
	large := strings.Repeat("x", maxWebhookOutput+1)
	async := <-drainWebhookStream(closedStream(agent.Event{Text: large}), false)
	if async.output != "" || async.err != nil {
		t.Fatalf("async drain retained output or failed: len=%d err=%v", len(async.output), async.err)
	}
	sync := <-drainWebhookStream(closedStream(agent.Event{Text: large}), true)
	if !errors.Is(sync.err, errWebhookOutputTooLarge) || len(sync.output) > maxWebhookOutput {
		t.Fatalf("sync drain = len %d err %v", len(sync.output), sync.err)
	}
}

func TestDrainWebhookStreamPreservesBusy(t *testing.T) {
	res := <-drainWebhookStream(closedStream(agent.Event{Err: fmt.Errorf("%w: session s1", agenterr.ErrSessionBusy)}), true)
	if !errors.Is(res.err, agenterr.ErrSessionBusy) {
		t.Fatalf("busy identity lost through drain: %v", res.err)
	}
}
