package plugins

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

// fakeBotChannel is a minimal channel whose Start blocks on the poll context, so
// tests can observe when polling stops.
type fakeBotChannel struct {
	name      string
	started   chan struct{}
	stopped   chan struct{}
	finalized chan struct{}
}

func newFakeBotChannel(name string) *fakeBotChannel {
	return &fakeBotChannel{name: name, started: make(chan struct{}), stopped: make(chan struct{}), finalized: make(chan struct{})}
}

func (c *fakeBotChannel) Name() string { return c.name }
func (c *fakeBotChannel) Start(ctx context.Context) error {
	close(c.started)
	<-ctx.Done()
	close(c.stopped)
	return ctx.Err()
}
func (c *fakeBotChannel) Stop()                                                 {}
func (c *fakeBotChannel) Notify(context.Context, pkgchannel.Notification) error { return nil }
func (c *fakeBotChannel) Finalize()                                             { close(c.finalized) }

// fakeNotifier records channel registrations.
type fakeNotifier struct {
	mu         sync.Mutex
	registered map[string]int
}

func newFakeNotifier() *fakeNotifier { return &fakeNotifier{registered: map[string]int{}} }

func (n *fakeNotifier) Register(ch pkgchannel.Channel) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.registered[ch.Name()]++
}

func (n *fakeNotifier) Unregister(name string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	delete(n.registered, name)
}

func (n *fakeNotifier) has(name string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.registered[name] > 0
}

// fakeBotHandler implements only the base Handler surface.
type fakeBotHandler struct{}

func (fakeBotHandler) HandleIncoming(context.Context, pkgchannel.IncomingMessage, string, string) (string, bool, *pkgchannel.ChatStream, error) {
	return "", false, nil, nil
}
func (fakeBotHandler) ListModels() []pkgchannel.ModelOption { return nil }
func (fakeBotHandler) SwitchModel(string, string) error     { return nil }
func (fakeBotHandler) ListAgents(context.Context, pkgchannel.IncomingMessage) ([]pkgchannel.AgentInfo, string, error) {
	return nil, "", nil
}

func (fakeBotHandler) SwitchAgent(context.Context, pkgchannel.IncomingMessage, string) error {
	return nil
}

func botTestSnapshot(now time.Time, state RuntimeState, message string, _ struct{}) RuntimeStatus {
	return RuntimeStatus{State: state, Message: message, UpdatedAt: now}
}

func waitClosed(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// TestBotRuntimeQuiesceTwoPhase proves the two-phase channel lifecycle: Quiesce
// stops polling but preserves the operation context and notifier registration
// and rejects a restart, while the final Stop cancels the operation context and
// unregisters the notifier.
func TestBotRuntimeQuiesceTwoPhase(t *testing.T) {
	notifier := newFakeNotifier()
	ch := newFakeBotChannel("tg")
	var builds int32
	var opCtx context.Context
	parent, cancelParent := context.WithCancel(context.Background())

	rt := NewBotManagedRuntime(BotRuntimeDeps[struct{}]{
		Parent:       parent,
		Handler:      fakeBotHandler{},
		Notifier:     notifier,
		Platform:     "telegram",
		DecodeConfig: func(map[string]any) (struct{}, error) { return struct{}{}, nil },
		NewChannel: func(struct{}, pkgchannel.Handler) (pkgchannel.Channel, error) {
			atomic.AddInt32(&builds, 1)
			return ch, nil
		},
		Snapshot: botTestSnapshot,
		WrapHandler: func(h pkgchannel.Handler, o context.Context) pkgchannel.Handler {
			opCtx = o
			return h
		},
	})

	state := PluginState{ID: "tg", Enabled: true}
	if err := rt.Start(context.Background(), state); err != nil {
		t.Fatalf("start: %v", err)
	}
	waitClosed(t, ch.started, "channel start")

	if opCtx == nil {
		t.Fatal("WrapHandler was not invoked with an operation context")
	}
	if !notifier.has("tg") {
		t.Fatal("expected channel registered with notifier after start")
	}

	// Phase 1: Quiesce stops polling but keeps the operation context and notifier.
	rt.Quiesce(context.Background())
	waitClosed(t, ch.stopped, "poll context cancellation")
	if opCtx.Err() != nil {
		t.Fatalf("Quiesce cancelled the operation context: %v", opCtx.Err())
	}
	if !notifier.has("tg") {
		t.Fatal("Quiesce unregistered the notifier; accepted work could not deliver")
	}
	select {
	case <-ch.finalized:
		t.Fatal("Quiesce finalized channel routing before accepted work drained")
	default:
	}
	cancelParent()
	if opCtx.Err() != nil {
		t.Fatalf("work-context cancellation reached accepted operation before final Stop: %v", opCtx.Err())
	}

	// Quiesce is terminal: a later Reconcile must not restart the channel.
	if err := rt.Apply(context.Background(), state); err == nil {
		t.Fatal("apply after quiesce must report that the runtime cannot restart")
	}
	if got := atomic.LoadInt32(&builds); got != 1 {
		t.Fatalf("Reconcile after Quiesce rebuilt the channel: builds=%d", got)
	}

	// Quiesce is idempotent.
	rt.Quiesce(context.Background())

	// Phase 2: final Stop cancels the operation context and unregisters.
	if err := rt.Stop(context.Background()); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if opCtx.Err() == nil {
		t.Fatal("final Stop did not cancel the operation context")
	}
	if notifier.has("tg") {
		t.Fatal("final Stop did not unregister the notifier")
	}
	waitClosed(t, ch.finalized, "channel finalization")
}
