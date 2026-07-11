package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/agent/agenterr"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/credential"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

// --- in-memory PATStore (only the methods Resolve/CreatePAT touch matter) ---

type memPATStore struct {
	byPublicID map[string]credential.PATRecord
}

func newMemPATStore() *memPATStore {
	return &memPATStore{byPublicID: map[string]credential.PATRecord{}}
}

func (m *memPATStore) CreatePAT(_ context.Context, rec credential.PATRecord) (credential.PATRecord, error) {
	m.byPublicID[rec.PublicID] = rec
	return rec, nil
}

func (m *memPATStore) GetPATByPublicID(_ context.Context, publicID string) (credential.PATRecord, error) {
	rec, ok := m.byPublicID[publicID]
	if !ok {
		return credential.PATRecord{}, io.EOF
	}
	return rec, nil
}

func (m *memPATStore) LookupUser(_ context.Context, userID string) (credential.Identity, error) {
	return credential.Identity{UserID: userID, Role: "user", IsActive: true}, nil
}

func (m *memPATStore) ListPATByUser(context.Context, string) ([]credential.PATRecord, error) {
	return nil, nil
}
func (m *memPATStore) RevokePAT(context.Context, string, string) (int64, error) { return 0, nil }
func (m *memPATStore) RevokePATByUser(context.Context, string) (int64, error)   { return 0, nil }
func (m *memPATStore) TouchPATLastUsed(context.Context, string) (int64, error)  { return 0, nil }

// --- fake config.Store: only GetChannel/GetAgent are exercised here ---

type webhookFakeStore struct {
	config.Store
	channel    config.Channel
	channelErr error
	agent      config.Agent
	agentErr   error
}

func (f webhookFakeStore) GetChannel(context.Context, string) (config.Channel, error) {
	return f.channel, f.channelErr
}

func (f webhookFakeStore) GetAgent(context.Context, string) (config.Agent, error) {
	return f.agent, f.agentErr
}

func mintPAT(t *testing.T, svc *credential.Service, user string, scopes []string) string {
	t.Helper()
	plaintext, _, err := svc.CreatePAT(context.Background(), user, "test", scopes, nil)
	if err != nil {
		t.Fatalf("mint PAT: %v", err)
	}
	return plaintext
}

// TestWebhookIngressGates covers the auth / validation branches that must reject
// before the agent ever runs. The engine-authorized happy path and the sync/async
// run need the full agent stack and are exercised by e2e/manual runs.
func TestWebhookIngressGates(t *testing.T) {
	patStore := newMemPATStore()
	credSvc := credential.NewService(credential.Config{
		PATs:   patStore,
		Users:  patStore,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	writeScoped := mintPAT(t, credSvc, "owner", []string{"agent:write"})
	readScoped := mintPAT(t, credSvc, "owner", []string{"goals:read"})

	webhookCh := config.Channel{
		ID: "wh1", Type: pkgchannel.PlatformWebhook, Enabled: true,
		AgentID: "agentA",
	}

	cases := []struct {
		name  string
		token string
		store webhookFakeStore
		want  int
	}{
		{"missing token", "", webhookFakeStore{channel: webhookCh}, 401},
		{"wrong scope", readScoped, webhookFakeStore{channel: webhookCh}, 403},
		{"channel not found", writeScoped, webhookFakeStore{channelErr: io.EOF}, 404},
		{"not a webhook type", writeScoped, webhookFakeStore{channel: config.Channel{ID: "wh1", Type: "telegram", Enabled: true}}, 404},
		{"disabled webhook", writeScoped, webhookFakeStore{channel: config.Channel{ID: "wh1", Type: pkgchannel.PlatformWebhook, Enabled: false, AgentID: "agentA"}}, 409},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{
				credResolver:   credSvc,
				store:          tc.store,
				log:            slog.New(slog.NewTextHandler(io.Discard, nil)),
				webhookLimiter: newWebhookLimiter(100, 100),
			}
			req := httptest.NewRequest("POST", "/webhooks/wh1", strings.NewReader("hello"))
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}
			req.SetPathValue("id", "wh1")
			rr := httptest.NewRecorder()
			s.handleWebhookIngress(rr, req)
			if rr.Code != tc.want {
				t.Fatalf("status = %d, want %d (body: %s)", rr.Code, tc.want, rr.Body.String())
			}
		})
	}
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

// TestDrainWebhookStreamPreservesBusy pins the error identity the busy branch
// depends on: a wrapped ErrSessionBusy must survive the drain so the handler
// can turn it into a 429 instead of a generic failure.
func TestDrainWebhookStreamPreservesBusy(t *testing.T) {
	stream := make(chan agent.Event, 1)
	stream <- agent.Event{Err: fmt.Errorf("%w: session s1", agenterr.ErrSessionBusy)}
	close(stream)

	res := <-drainWebhookStream(stream)
	if !errors.Is(res.err, agenterr.ErrSessionBusy) {
		t.Fatalf("busy identity lost through drain: %v", res.err)
	}
}

func TestPeekWebhookResult(t *testing.T) {
	// An immediate result (e.g. a busy rejection, emitted before any work
	// starts) is caught inside the window.
	ready := make(chan webhookResult, 1)
	ready <- webhookResult{err: agenterr.ErrSessionBusy}
	res, ok := peekWebhookResult(ready, time.Second)
	if !ok || !errors.Is(res.err, agenterr.ErrSessionBusy) {
		t.Fatalf("expected immediate busy result, ok=%v err=%v", ok, res.err)
	}

	// A still-running stream yields nothing within the window.
	if _, ok := peekWebhookResult(make(chan webhookResult), 10*time.Millisecond); ok {
		t.Fatal("expected no result from a running stream")
	}
}
