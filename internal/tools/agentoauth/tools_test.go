package agentoauth

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/credentials"
	"github.com/CherryHQ/stella/internal/memory"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

// fakeService drives the watcher deterministically: PollFlow returns the queued
// outcome on its first call.
type fakeService struct {
	startURL  string
	startCode string
	startErr  error

	pollDone  bool
	pollState string
	pollErr   error
	pollCalls atomic.Int32
}

func (f *fakeService) GetProviderStatuses(context.Context, string) []credentials.ProviderStatus {
	return []credentials.ProviderStatus{{Provider: "github"}}
}

func (f *fakeService) StartFlow(context.Context, string, string) (credentials.FlowStatus, error) {
	if f.startErr != nil {
		return credentials.FlowStatus{}, f.startErr
	}
	return credentials.FlowStatus{
		FlowID:          "flow-1",
		VerificationURI: f.startURL,
		UserCode:        f.startCode,
	}, nil
}

func (f *fakeService) PollFlow(context.Context, string, string, string) (credentials.FlowStatus, bool, error) {
	f.pollCalls.Add(1)
	return credentials.FlowStatus{State: f.pollState}, f.pollDone, f.pollErr
}

func (f *fakeService) Disconnect(context.Context, string, string) error { return nil }

// recordingNotifier captures notifications and signals on each delivery.
type recordingNotifier struct {
	mu   sync.Mutex
	msgs []string
	ch   chan struct{}
}

func newRecordingNotifier() *recordingNotifier {
	return &recordingNotifier{ch: make(chan struct{}, 8)}
}

func (n *recordingNotifier) NotifyUser(_ context.Context, _ string, note pkgchannel.Notification) error {
	n.mu.Lock()
	n.msgs = append(n.msgs, note.Text)
	n.mu.Unlock()
	n.ch <- struct{}{}
	return nil
}

func (n *recordingNotifier) texts() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.msgs...)
}

func implFor(svc oauthService, n Notifier) *impl {
	return &impl{
		svc:      svc,
		notifier: n,
		bg:       func(fn func(ctx context.Context)) { go fn(context.Background()) },
	}
}

func ctxUserAgent(userID, agentID string) context.Context {
	ctx := memory.WithUserID(context.Background(), userID)
	return memory.WithAgentID(ctx, agentID)
}

func waitNotify(t *testing.T, n *recordingNotifier) {
	t.Helper()
	select {
	case <-n.ch:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for notification")
	}
}

func TestSchemasHaveNoIdentityProps(t *testing.T) {
	for _, def := range []map[string]any{
		providersDef().InputSchema, statusDef().InputSchema,
		connectDef().InputSchema, disconnectDef().InputSchema,
	} {
		props, _ := def["properties"].(map[string]any)
		for _, k := range []string{"user", "user_id", "agent", "agent_id"} {
			if _, ok := props[k]; ok {
				t.Errorf("schema exposes identity prop %q", k)
			}
		}
	}
}

func TestConnectReturnsImmediatelyAndNotifiesOnce(t *testing.T) {
	t.Parallel()
	svc := &fakeService{startURL: "https://example.com/device", startCode: "ABCD", pollDone: true}
	n := newRecordingNotifier()
	tl := implFor(svc, n)

	out, err := tl.connect(ctxUserAgent("u1", "a1"), map[string]any{"provider": "github"})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	// The verification URL must come back in the immediate response, not block
	// on the human authorizing.
	if !strings.Contains(out, "https://example.com/device") {
		t.Errorf("connect output missing verification_uri: %s", out)
	}
	if !strings.Contains(out, "ABCD") {
		t.Errorf("connect output missing user_code: %s", out)
	}

	waitNotify(t, n)
	// Give any (erroneous) second notification a chance to land.
	time.Sleep(50 * time.Millisecond)
	if got := n.texts(); len(got) != 1 {
		t.Fatalf("want exactly 1 notification, got %d: %v", len(got), got)
	}
	if !strings.Contains(n.texts()[0], "Connected to github") {
		t.Errorf("unexpected notification text: %q", n.texts()[0])
	}
}

func TestConnectNotifiesOnFailure(t *testing.T) {
	t.Parallel()
	svc := &fakeService{startURL: "https://example.com/device", pollState: "failed"}
	n := newRecordingNotifier()
	tl := implFor(svc, n)

	if _, err := tl.connect(ctxUserAgent("u1", "a1"), map[string]any{"provider": "github"}); err != nil {
		t.Fatalf("connect: %v", err)
	}
	waitNotify(t, n)
	if got := n.texts()[0]; !strings.Contains(got, "failed") {
		t.Errorf("want failure notification, got %q", got)
	}
}

// The watcher must run on a server-lifetime ctx, not the request ctx. Cancelling
// the request ctx after connect returns must not stop the notification.
func TestWatcherSurvivesRequestCancel(t *testing.T) {
	t.Parallel()
	svc := &fakeService{startURL: "https://example.com/device", pollDone: true}
	n := newRecordingNotifier()
	tl := implFor(svc, n)

	reqCtx, cancel := context.WithCancel(ctxUserAgent("u1", "a1"))
	if _, err := tl.connect(reqCtx, map[string]any{"provider": "github"}); err != nil {
		t.Fatalf("connect: %v", err)
	}
	cancel() // end the agent turn

	waitNotify(t, n)
	if got := n.texts(); len(got) != 1 || !strings.Contains(got[0], "Connected to github") {
		t.Fatalf("watcher did not survive request cancel: %v", got)
	}
}

func TestConnectRequiresProvider(t *testing.T) {
	t.Parallel()
	tl := implFor(&fakeService{}, newRecordingNotifier())
	if _, err := tl.connect(ctxUserAgent("u1", "a1"), map[string]any{}); err == nil {
		t.Error("connect without provider: want error")
	}
}

func TestProvidersRequiresIdentity(t *testing.T) {
	t.Parallel()
	tl := implFor(&fakeService{}, newRecordingNotifier())
	if _, err := tl.providers(context.Background(), nil); err == nil {
		t.Error("providers without identity: want error")
	}
}
