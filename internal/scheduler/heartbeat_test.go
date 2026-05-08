package scheduler

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/notify"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

type chatCall struct {
	sessionID string
	message   string
	model     string
}

type fakeNotifier struct {
	calls []pkgchannel.Notification
	err   error
}

func (f *fakeNotifier) Notify(_ context.Context, n pkgchannel.Notification) error {
	f.calls = append(f.calls, n)
	return f.err
}

func makeChatFunc(calls *[]chatCall, responses map[string][]agent.Event) ChatFunc {
	return func(_ context.Context, sessionID, message, model string) <-chan agent.Event {
		*calls = append(*calls, chatCall{
			sessionID: sessionID,
			message:   message,
			model:     model,
		})

		out := make(chan agent.Event, len(responses[sessionID]))
		for _, evt := range responses[sessionID] {
			out <- evt
		}
		close(out)
		return out
	}
}

func newHeartbeatTestService(t *testing.T, cfg HeartbeatConfig, calls *[]chatCall, responses map[string][]agent.Event, notifier notify.Notifier) *Service {
	t.Helper()
	db := testDB(t)
	svc, err := New(db)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := svc.StartEphemeral(context.Background()); err != nil {
		t.Fatalf("StartEphemeral: %v", err)
	}
	t.Cleanup(func() { _ = svc.Stop() })
	svc.SetHeartbeat(cfg, makeChatFunc(calls, responses), notifier)
	return svc
}

func TestHeartbeatPollSkipsMissingFile(t *testing.T) {
	var calls []chatCall
	notifier := &fakeNotifier{}
	svc := newHeartbeatTestService(t, HeartbeatConfig{
		File:      filepath.Join(t.TempDir(), "HEARTBEAT.md"),
		FastModel: "fast-model",
	}, &calls, nil, notifier)

	if err := svc.heartbeatPoll(context.Background()); err != nil {
		t.Fatalf("heartbeatPoll: %v", err)
	}

	if len(calls) != 0 {
		t.Fatalf("expected no chat calls, got %d", len(calls))
	}
	if len(notifier.calls) != 0 {
		t.Fatalf("expected no notifications, got %d", len(notifier.calls))
	}
}

func TestHeartbeatPollSkipDecisionUsesFastModel(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "HEARTBEAT.md")
	if err := os.WriteFile(path, []byte("Check if anything needs doing."), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	var calls []chatCall
	notifier := &fakeNotifier{}
	svc := newHeartbeatTestService(t, HeartbeatConfig{
		File:      path,
		FastModel: "fast-model",
	}, &calls, map[string][]agent.Event{
		heartbeatDecisionSessionID: {{Text: `{"action":"skip","reason":"nothing pending"}`}},
	}, notifier)

	if err := svc.heartbeatPoll(context.Background()); err != nil {
		t.Fatalf("heartbeatPoll: %v", err)
	}

	if len(calls) != 1 {
		t.Fatalf("expected 1 chat call, got %d", len(calls))
	}
	if calls[0].sessionID != heartbeatDecisionSessionID {
		t.Fatalf("sessionID = %q, want %q", calls[0].sessionID, heartbeatDecisionSessionID)
	}
	if calls[0].model != "fast-model" {
		t.Fatalf("model = %q, want %q", calls[0].model, "fast-model")
	}
	if len(notifier.calls) != 0 {
		t.Fatalf("expected no notifications, got %d", len(notifier.calls))
	}
}

func TestHeartbeatPollRunDecisionExecutesAndNotifies(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "HEARTBEAT.md")
	if err := os.WriteFile(path, []byte("Review the workspace and report actionable changes."), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	var calls []chatCall
	notifier := &fakeNotifier{}
	svc := newHeartbeatTestService(t, HeartbeatConfig{
		File:      path,
		FastModel: "fast-model",
	}, &calls, map[string][]agent.Event{
		heartbeatDecisionSessionID: {{Text: `{"action":"run","reason":"new work detected"}`}},
		heartbeatMainSessionID:     {{Text: "Action complete."}},
	}, notifier)

	if err := svc.heartbeatPoll(context.Background()); err != nil {
		t.Fatalf("heartbeatPoll: %v", err)
	}

	if len(calls) != 2 {
		t.Fatalf("expected 2 chat calls, got %d", len(calls))
	}
	if calls[0].sessionID != heartbeatDecisionSessionID || calls[0].model != "fast-model" {
		t.Fatalf("unexpected decision call: %+v", calls[0])
	}
	if calls[1].sessionID != heartbeatMainSessionID || calls[1].model != "" {
		t.Fatalf("unexpected main call: %+v", calls[1])
	}
	if len(notifier.calls) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(notifier.calls))
	}
	if notifier.calls[0].Text != "*Heartbeat*\n\nAction complete." {
		t.Fatalf("notification text = %q", notifier.calls[0].Text)
	}
}

func TestHeartbeatPollFailsWhenDecisionUsesTools(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "HEARTBEAT.md")
	if err := os.WriteFile(path, []byte("Ping if needed."), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	var calls []chatCall
	svc := newHeartbeatTestService(t, HeartbeatConfig{
		File:      path,
		FastModel: "fast-model",
	}, &calls, map[string][]agent.Event{
		heartbeatDecisionSessionID: {{
			ToolUse: &agent.ToolUseEvent{Tool: "bash", Status: "running"},
		}},
	}, &fakeNotifier{})

	err := svc.heartbeatPoll(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if err.Error() != "heartbeat decision attempted to use tools" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestHeartbeatPollReturnsNotifierError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "HEARTBEAT.md")
	if err := os.WriteFile(path, []byte("Act now."), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	var calls []chatCall
	notifier := &fakeNotifier{err: errors.New("notify failed")}
	svc := newHeartbeatTestService(t, HeartbeatConfig{
		File:      path,
		FastModel: "fast-model",
	}, &calls, map[string][]agent.Event{
		heartbeatDecisionSessionID: {{Text: `{"action":"run","reason":"do it"}`}},
		heartbeatMainSessionID:     {{Text: "Done."}},
	}, notifier)

	err := svc.heartbeatPoll(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if err.Error() != "notify heartbeat result: notify failed" {
		t.Fatalf("unexpected error: %v", err)
	}
}
