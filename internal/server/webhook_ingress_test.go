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
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/agent/agenterr"
	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/channel"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/pluginhost"
	"github.com/CherryHQ/stella/internal/webhook"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

type ingressStore struct {
	config.Store
	channel config.Channel
}

func (s ingressStore) GetChannel(context.Context, string) (config.Channel, error) {
	return s.channel, nil
}

type fakeWebhookIngress struct {
	inv           webhook.Invocation
	resolveErr    error
	validateErr   error
	claims        int
	releases      int
	claimActive   bool
	validateRuns  int
	releaseCtxErr error
	resolvedToken string
}

func (f *fakeWebhookIngress) ResolveCapability(_ context.Context, raw string) (webhook.Invocation, error) {
	f.resolvedToken = raw
	return f.inv, f.resolveErr
}

func (f *fakeWebhookIngress) ValidateGitHub(webhook.Invocation, http.Header, []byte, webhook.GitHubPolicy) (webhook.GitHubDelivery, error) {
	f.validateRuns++
	return webhook.GitHubDelivery{}, f.validateErr
}

func (f *fakeWebhookIngress) ClaimGitHubDelivery(context.Context, webhook.GitHubDelivery) (bool, error) {
	f.claims++
	if f.claimActive {
		return false, nil
	}
	f.claimActive = true
	return true, nil
}

func (f *fakeWebhookIngress) ReleaseGitHubDelivery(ctx context.Context, _ webhook.GitHubDelivery) (bool, error) {
	f.releases++
	f.releaseCtxErr = ctx.Err()
	if f.releaseCtxErr != nil {
		return false, f.releaseCtxErr
	}
	f.claimActive = false
	return true, nil
}

type fakeWebhookRunPort struct{ run webhookAgentRun }

func (p fakeWebhookRunPort) Get(string) webhookAgentRun { return p.run }

type fakeWebhookAgentRun struct {
	info       session.Info
	sessionErr error
	admitErr   error
	stream     <-chan agent.Event
	newCalls   int
	chatCalls  int
	lastAuth   authz.Authority
	lastReq    agent.ChatRequest
}

func (r *fakeWebhookAgentRun) ResolvePrivateChannelSession(_ context.Context, authority authz.Authority, _ string, _, _ string, _ session.Channel) (session.Info, error) {
	r.lastAuth = authority
	return r.info, r.sessionErr
}

func (r *fakeWebhookAgentRun) NewSession(_ context.Context, authority authz.Authority, _, _, _ string, _ session.Kind, _ session.Channel) (session.Info, error) {
	r.newCalls++
	r.lastAuth = authority
	return r.info, r.sessionErr
}

func (r *fakeWebhookAgentRun) ChatAdmitted(_ context.Context, req agent.ChatRequest) (<-chan agent.Event, error) {
	r.chatCalls++
	r.lastReq = req
	return r.stream, r.admitErr
}

func closedWebhookStream(events ...agent.Event) <-chan agent.Event {
	out := make(chan agent.Event, len(events))
	for _, event := range events {
		out <- event
	}
	close(out)
	return out
}

func newIngressServer(t *testing.T, provider webhook.Provider, ingress *fakeWebhookIngress, run webhookAgentRun, limiter *webhookLimiter) *Server {
	t.Helper()
	authority, err := authz.NewAgentAuthority("owner-1", "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	ingress.inv = webhook.Invocation{Endpoint: webhook.Endpoint{ID: "endpoint-1", ChannelID: "channel-1", OwnerUserID: "owner-1", Provider: provider}, AgentID: "agent-1", Authority: authority}
	configJSON := `{"provider":"generic"}`
	if provider == webhook.ProviderGitHub {
		configJSON = `{"provider":"github","github_events":["push"],"github_repositories":["acme/repo"]}`
	}
	return &Server{
		channelResolver: channel.NewRuntimeResolver(ingressStore{channel: config.Channel{ID: "channel-1", Type: pkgchannel.PlatformWebhook, AgentID: "agent-1", Enabled: true, Config: configJSON}}),
		pluginHost:      &pluginhost.Host{},
		webhookIngress:  ingress,
		webhookRun:      fakeWebhookRunPort{run: run},
		webhookLimiter:  limiter,
		log:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		runtimeCtx:      context.Background(),
	}
}

func TestRootMuxRedactsEveryWebhookSubtreeShape(t *testing.T) {
	const capability = "stella_whk_publicID_very-secret-fragment"
	var logs bytes.Buffer
	s := &Server{
		log:            slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		webhookIngress: &fakeWebhookIngress{resolveErr: webhook.ErrNotFound},
		runtimeCtx:     context.Background(),
	}
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		_ = provider.Shutdown(t.Context())
		otel.SetTracerProvider(previous)
	})

	root := NewRootMux(s)
	for _, tc := range []struct {
		name, method, path string
		wantSpans          int
	}{
		{"canonical post", http.MethodPost, "/webhooks/" + capability, 1},
		{"wrong method", http.MethodGet, "/webhooks/" + capability, 0},
		{"trailing slash", http.MethodPost, "/webhooks/" + capability + "/", 0},
		{"extra segment", http.MethodPost, "/webhooks/" + capability + "/extra", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs.Reset()
			before := len(recorder.Ended())
			rr := httptest.NewRecorder()
			root.ServeHTTP(rr, httptest.NewRequest(tc.method, tc.path, nil))
			if rr.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404", rr.Code)
			}
			if tc.name == "canonical post" && s.webhookIngress.(*fakeWebhookIngress).resolvedToken != capability {
				t.Fatalf("handler capability = %q, want raw route value", s.webhookIngress.(*fakeWebhookIngress).resolvedToken)
			}
			assertWebhookSecretAbsent(t, logs.String(), capability)
			spans := recorder.Ended()[before:]
			if len(spans) != tc.wantSpans {
				t.Fatalf("spans = %d, want %d", len(spans), tc.wantSpans)
			}
			for _, span := range spans {
				assertWebhookSecretAbsent(t, span.Name(), capability)
				for _, attribute := range span.Attributes() {
					assertWebhookSecretAbsent(t, fmt.Sprint(attribute), capability)
				}
			}
		})
	}
}

func assertWebhookSecretAbsent(t *testing.T, got, capability string) {
	t.Helper()
	for _, forbidden := range []string{capability, "publicID", "very-secret-fragment", "/webhooks/" + capability} {
		if strings.Contains(got, forbidden) {
			t.Errorf("secret observability data leaks %q: %q", forbidden, got)
		}
	}
}

func TestWebhookIngressUsesFixedCapabilityIdentity(t *testing.T) {
	ingress := &fakeWebhookIngress{}
	run := &fakeWebhookAgentRun{info: session.Info{ID: "session-1"}, stream: closedWebhookStream()}
	s := newIngressServer(t, webhook.ProviderGeneric, ingress, run, newWebhookLimiter(100, 100))
	req := httptest.NewRequest(http.MethodPost, "/webhooks/token", strings.NewReader("hello"))
	req.Header.Set("Authorization", "Bearer another-users-pat")
	req.SetPathValue("capability", "token")
	rr := httptest.NewRecorder()
	s.handleWebhookIngress(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %s", rr.Code, rr.Body.String())
	}
	if run.chatCalls != 1 || run.lastReq.UserID != "owner-1" || run.lastReq.AgentID != "agent-1" || run.lastReq.Authority != ingress.inv.Authority {
		t.Fatalf("run identity = %+v, authority=%v", run.lastReq, run.lastReq.Authority)
	}
}

func TestGitHubWebhookIngressAdmissionAndDeduplication(t *testing.T) {
	newRequest := func() *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/webhooks/token", strings.NewReader(`{"repository":{"full_name":"acme/repo"}}`))
		req.SetPathValue("capability", "token")
		return req
	}
	t.Run("invalid signature never claims or runs", func(t *testing.T) {
		ingress := &fakeWebhookIngress{validateErr: webhook.ErrInvalidGitHubDelivery}
		run := &fakeWebhookAgentRun{info: session.Info{ID: "s"}, stream: closedWebhookStream()}
		s := newIngressServer(t, webhook.ProviderGitHub, ingress, run, newWebhookLimiter(1, 1))
		rr := httptest.NewRecorder()
		s.handleWebhookIngress(rr, newRequest())
		if rr.Code == http.StatusOK || ingress.claims != 0 || run.newCalls != 0 || run.chatCalls != 0 {
			t.Fatalf("invalid delivery status/side effects = %d/%d/%d/%d", rr.Code, ingress.claims, run.newCalls, run.chatCalls)
		}
		// Invalid validation did not consume the sole accepted-call token.
		ingress.validateErr = nil
		rr = httptest.NewRecorder()
		s.handleWebhookIngress(rr, newRequest())
		if rr.Code != http.StatusAccepted {
			t.Fatalf("validated delivery after invalid status = %d", rr.Code)
		}
	})
	t.Run("duplicate starts exactly one turn", func(t *testing.T) {
		ingress := &fakeWebhookIngress{}
		run := &fakeWebhookAgentRun{info: session.Info{ID: "s"}, stream: closedWebhookStream()}
		s := newIngressServer(t, webhook.ProviderGitHub, ingress, run, newWebhookLimiter(100, 100))
		for range 2 {
			rr := httptest.NewRecorder()
			s.handleWebhookIngress(rr, newRequest())
			if rr.Code != http.StatusAccepted {
				t.Fatalf("status = %d, want 202", rr.Code)
			}
		}
		if run.chatCalls != 1 || ingress.claims != 2 || ingress.releases != 0 {
			t.Fatalf("turns/claims/releases = %d/%d/%d", run.chatCalls, ingress.claims, ingress.releases)
		}
	})
	t.Run("not admitted releases so redelivery retries", func(t *testing.T) {
		ingress := &fakeWebhookIngress{}
		run := &fakeWebhookAgentRun{info: session.Info{ID: "s"}, admitErr: agenterr.ErrSessionBusy}
		s := newIngressServer(t, webhook.ProviderGitHub, ingress, run, newWebhookLimiter(100, 100))
		rr := httptest.NewRecorder()
		s.handleWebhookIngress(rr, newRequest())
		if rr.Code < 500 || ingress.releases != 1 {
			t.Fatalf("busy status/releases = %d/%d", rr.Code, ingress.releases)
		}
		run.admitErr = nil
		run.stream = closedWebhookStream()
		rr = httptest.NewRecorder()
		s.handleWebhookIngress(rr, newRequest())
		if rr.Code != http.StatusAccepted || run.chatCalls != 2 {
			t.Fatalf("retry status/turns = %d/%d", rr.Code, run.chatCalls)
		}
	})
	t.Run("request cancellation does not cancel pre-admission release", func(t *testing.T) {
		ingress := &fakeWebhookIngress{}
		run := &fakeWebhookAgentRun{info: session.Info{ID: "s"}, admitErr: agenterr.ErrSessionBusy}
		s := newIngressServer(t, webhook.ProviderGitHub, ingress, run, newWebhookLimiter(100, 100))
		ctx, cancel := context.WithCancel(context.Background())
		req := newRequest().WithContext(ctx)
		cancel()
		rr := httptest.NewRecorder()
		s.handleWebhookIngress(rr, req)
		if rr.Code != http.StatusServiceUnavailable || ingress.releases != 1 || ingress.releaseCtxErr != nil || ingress.claimActive {
			t.Fatalf("cancelled request status/releases/release-context/claim = %d/%d/%v/%t", rr.Code, ingress.releases, ingress.releaseCtxErr, ingress.claimActive)
		}
	})
	t.Run("admitted fast failure retains claim", func(t *testing.T) {
		ingress := &fakeWebhookIngress{}
		run := &fakeWebhookAgentRun{info: session.Info{ID: "s"}, stream: closedWebhookStream(agent.Event{Err: errors.New("runner failed")})}
		s := newIngressServer(t, webhook.ProviderGitHub, ingress, run, newWebhookLimiter(100, 100))
		rr := httptest.NewRecorder()
		s.handleWebhookIngress(rr, newRequest())
		if rr.Code != http.StatusAccepted || ingress.releases != 0 {
			t.Fatalf("fast failure status/releases = %d/%d", rr.Code, ingress.releases)
		}
	})
	t.Run("session access failure releases claim", func(t *testing.T) {
		ingress := &fakeWebhookIngress{}
		run := &fakeWebhookAgentRun{info: session.Info{ID: "s"}, admitErr: errors.New("session access denied")}
		s := newIngressServer(t, webhook.ProviderGitHub, ingress, run, newWebhookLimiter(100, 100))
		rr := httptest.NewRecorder()
		s.handleWebhookIngress(rr, newRequest())
		if rr.Code < 500 || ingress.releases != 1 {
			t.Fatalf("access failure status/releases = %d/%d", rr.Code, ingress.releases)
		}
	})
}

func TestDrainWebhookStream(t *testing.T) {
	stream := make(chan agent.Event, 4)
	stream <- agent.Event{Text: "Hello, "}
	stream <- agent.Event{Text: "world"}
	stream <- agent.Event{Reasoning: "ignored"}
	close(stream)

	res := <-drainWebhookStream(stream)
	if res.err != nil {
		t.Fatalf("unexpected err: %v", res.err)
	}
	if res.output != "Hello, world" {
		t.Fatalf("output = %q, want %q", res.output, "Hello, world")
	}
}
