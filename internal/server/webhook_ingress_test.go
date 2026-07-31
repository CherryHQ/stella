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
	"strings"
	"sync/atomic"
	"testing"

	"github.com/CherryHQ/stella/internal/agent"
	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	"github.com/CherryHQ/stella/internal/agent/agenterr"
	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/channel"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/pluginhost"
	"github.com/CherryHQ/stella/internal/webhook"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
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

// webhookFakeStore serves a fixed webhook channel config to the channel resolver.
type webhookFakeStore struct {
	config.Store
	channel config.Channel
}

func (f webhookFakeStore) GetChannel(context.Context, string) (config.Channel, error) {
	return f.channel, nil
}

func newIngressServer(t *testing.T, ingress webhookIngressPort, run *fakeAgentRun) *Server {
	t.Helper()
	return newIngressServerConfig(t, ingress, run, "{}")
}

func newIngressServerConfig(t *testing.T, ingress webhookIngressPort, run *fakeAgentRun, cfgJSON string) *Server {
	t.Helper()
	return &Server{
		log:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		webhookLimiter:  newWebhookLimiter(100, 100),
		webhookIngress:  ingress,
		webhookRun:      fakeRunPort{run: run},
		channelResolver: channel.NewRuntimeResolver(webhookFakeStore{channel: config.Channel{ID: "c", Type: pkgchannel.PlatformWebhook, Enabled: true, AgentID: "agentA", Config: cfgJSON}}),
		pluginHost:      &pluginhost.Host{},
		runtimeCtx:      context.Background(),
	}
}

func capabilityRequest(t *testing.T, wait *bool) *http.Request {
	t.Helper()
	ctx := context.WithValue(context.Background(), webhookCapabilityCtxKey{}, "stella_whk_capability")
	if wait != nil {
		ctx = context.WithValue(ctx, webhookWaitCtxKey{}, *wait)
	}
	req := httptest.NewRequest(http.MethodPost, sanitizedWebhookPath, strings.NewReader("hello world")).WithContext(ctx)
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
		cand: webhook.Candidate{EndpointID: "c"},
		admit: func(ctx context.Context, _ webhook.Candidate, cb webhook.AdmitCallback) error {
			return cb(ctx, webhook.AdmittedInvocation{ChannelID: "c", OwnerUserID: "owner-1", AgentID: "agentA", Provider: webhook.ProviderGeneric, Authority: authority})
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
	ingress := &fakeIngress{cand: webhook.Candidate{EndpointID: "c"}, admit: func(ctx context.Context, _ webhook.Candidate, cb webhook.AdmitCallback) error {
		return cb(ctx, webhook.AdmittedInvocation{ChannelID: "c", OwnerUserID: "owner-1", AgentID: "agentA", Authority: authority})
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
	for _, admitErr := range []error{webhook.ErrNotFound, webhook.ErrOwnerAgentForbidden} {
		t.Run(admitErr.Error(), func(t *testing.T) {
			run := &fakeAgentRun{}
			ingress := &fakeIngress{cand: webhook.Candidate{EndpointID: "c"}, admit: func(context.Context, webhook.Candidate, webhook.AdmitCallback) error {
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
	ingress := &fakeIngress{cand: webhook.Candidate{EndpointID: "c"}, admit: func(ctx context.Context, _ webhook.Candidate, cb webhook.AdmitCallback) error {
		return cb(ctx, webhook.AdmittedInvocation{ChannelID: "c", OwnerUserID: "owner-1", AgentID: "agentA", Authority: authority})
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
	ingress := &fakeIngress{cand: webhook.Candidate{EndpointID: "c"}, admit: func(ctx context.Context, _ webhook.Candidate, cb webhook.AdmitCallback) error {
		return cb(ctx, webhook.AdmittedInvocation{ChannelID: "c", OwnerUserID: "owner-1", AgentID: "agentA", Authority: authority})
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
			ingress := &fakeIngress{cand: webhook.Candidate{EndpointID: "c"}, admit: func(ctx context.Context, _ webhook.Candidate, cb webhook.AdmitCallback) error {
				return cb(ctx, webhook.AdmittedInvocation{ChannelID: "c", OwnerUserID: "owner-1", AgentID: "agentA", Authority: authority})
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
	ingress := &fakeIngress{cand: webhook.Candidate{EndpointID: "c"}, admit: func(ctx context.Context, _ webhook.Candidate, cb webhook.AdmitCallback) error {
		return cb(ctx, webhook.AdmittedInvocation{ChannelID: "c", OwnerUserID: "owner-1", AgentID: "agentA", Authority: authority})
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
	ingress := &fakeIngress{cand: webhook.Candidate{EndpointID: "c"}, admit: func(ctx context.Context, _ webhook.Candidate, cb webhook.AdmitCallback) error {
		return cb(ctx, webhook.AdmittedInvocation{ChannelID: "c", OwnerUserID: "owner-1", AgentID: "agentA", Authority: authority})
	}}
	s := newIngressServerConfig(t, ingress, run, `{"session_mode":"persistent"}`)

	rr := httptest.NewRecorder()
	s.handleWebhookIngress(rr, capabilityRequest(t, nil))

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
	ingress := &fakeIngress{cand: webhook.Candidate{EndpointID: "c"}, admit: func(ctx context.Context, _ webhook.Candidate, cb webhook.AdmitCallback) error {
		return cb(ctx, webhook.AdmittedInvocation{ChannelID: "c", OwnerUserID: "owner-1", AgentID: "agentA", Authority: authority})
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

func TestDrainWebhookStream(t *testing.T) {
	res := <-drainWebhookStream(closedStream(agent.Event{Text: "Hello, "}, agent.Event{Text: "world"}, agent.Event{Reasoning: "ignored"}))
	if res.err != nil {
		t.Fatalf("unexpected err: %v", res.err)
	}
	if res.output != "Hello, world" {
		t.Fatalf("output = %q, want %q", res.output, "Hello, world")
	}
}

// TestDrainWebhookStreamPreservesBusy pins the error identity the busy branch
// depends on: a wrapped ErrSessionBusy must survive the drain.
func TestDrainWebhookStreamPreservesBusy(t *testing.T) {
	res := <-drainWebhookStream(closedStream(agent.Event{Err: fmt.Errorf("%w: session s1", agenterr.ErrSessionBusy)}))
	if !errors.Is(res.err, agenterr.ErrSessionBusy) {
		t.Fatalf("busy identity lost through drain: %v", res.err)
	}
}
