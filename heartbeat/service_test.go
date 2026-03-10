package heartbeat

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/vaayne/anna/agent/runner"
	"github.com/vaayne/anna/channel"
)

type chatCall struct {
	sessionID string
	message   string
	model     string
}

type fakeNotifier struct {
	calls []channel.Notification
	err   error
}

func (f *fakeNotifier) Notify(_ context.Context, n channel.Notification) error {
	f.calls = append(f.calls, n)
	return f.err
}

func makeChatFunc(calls *[]chatCall, responses map[string][]runner.Event) ChatFunc {
	return func(_ context.Context, sessionID, message, model string) <-chan runner.Event {
		*calls = append(*calls, chatCall{
			sessionID: sessionID,
			message:   message,
			model:     model,
		})

		out := make(chan runner.Event, len(responses[sessionID]))
		for _, evt := range responses[sessionID] {
			out <- evt
		}
		close(out)
		return out
	}
}

func TestPollSkipsMissingHeartbeatFile(t *testing.T) {
	var calls []chatCall
	notifier := &fakeNotifier{}
	svc := New(Config{
		File:      filepath.Join(t.TempDir(), "HEARTBEAT.md"),
		FastModel: "fast-model",
	}, makeChatFunc(&calls, nil), notifier)

	if err := svc.Poll(context.Background()); err != nil {
		t.Fatalf("Poll: %v", err)
	}

	if len(calls) != 0 {
		t.Fatalf("expected no chat calls, got %d", len(calls))
	}
	if len(notifier.calls) != 0 {
		t.Fatalf("expected no notifications, got %d", len(notifier.calls))
	}
}

func TestPollSkipDecisionUsesFastModel(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "HEARTBEAT.md")
	if err := os.WriteFile(path, []byte("Check if anything needs doing."), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	var calls []chatCall
	notifier := &fakeNotifier{}
	svc := New(Config{
		File:      path,
		FastModel: "fast-model",
	}, makeChatFunc(&calls, map[string][]runner.Event{
		decisionSessionID: {{Text: `{"action":"skip","reason":"nothing pending"}`}},
	}), notifier)

	if err := svc.Poll(context.Background()); err != nil {
		t.Fatalf("Poll: %v", err)
	}

	if len(calls) != 1 {
		t.Fatalf("expected 1 chat call, got %d", len(calls))
	}
	if calls[0].sessionID != decisionSessionID {
		t.Fatalf("sessionID = %q, want %q", calls[0].sessionID, decisionSessionID)
	}
	if calls[0].model != "fast-model" {
		t.Fatalf("model = %q, want %q", calls[0].model, "fast-model")
	}
	if len(notifier.calls) != 0 {
		t.Fatalf("expected no notifications, got %d", len(notifier.calls))
	}
}

func TestPollRunDecisionExecutesAndNotifies(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "HEARTBEAT.md")
	if err := os.WriteFile(path, []byte("Review the workspace and report actionable changes."), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	var calls []chatCall
	notifier := &fakeNotifier{}
	svc := New(Config{
		File:      path,
		FastModel: "fast-model",
	}, makeChatFunc(&calls, map[string][]runner.Event{
		decisionSessionID: {{Text: `{"action":"run","reason":"new work detected"}`}},
		mainSessionID:     {{Text: "Action complete."}},
	}), notifier)

	if err := svc.Poll(context.Background()); err != nil {
		t.Fatalf("Poll: %v", err)
	}

	if len(calls) != 2 {
		t.Fatalf("expected 2 chat calls, got %d", len(calls))
	}
	if calls[0].sessionID != decisionSessionID || calls[0].model != "fast-model" {
		t.Fatalf("unexpected decision call: %+v", calls[0])
	}
	if calls[1].sessionID != mainSessionID || calls[1].model != "" {
		t.Fatalf("unexpected main call: %+v", calls[1])
	}
	if len(notifier.calls) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(notifier.calls))
	}
	if notifier.calls[0].Text != "*Heartbeat*\n\nAction complete." {
		t.Fatalf("notification text = %q", notifier.calls[0].Text)
	}
}

func TestPollFailsWhenDecisionUsesTools(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "HEARTBEAT.md")
	if err := os.WriteFile(path, []byte("Ping if needed."), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	var calls []chatCall
	svc := New(Config{
		File:      path,
		FastModel: "fast-model",
	}, makeChatFunc(&calls, map[string][]runner.Event{
		decisionSessionID: {{
			ToolUse: &runner.ToolUseEvent{Tool: "bash", Status: "running"},
		}},
	}), &fakeNotifier{})

	err := svc.Poll(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if err.Error() != "heartbeat decision attempted to use tools" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPollReturnsNotifierError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "HEARTBEAT.md")
	if err := os.WriteFile(path, []byte("Act now."), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	var calls []chatCall
	notifier := &fakeNotifier{err: errors.New("notify failed")}
	svc := New(Config{
		File:      path,
		FastModel: "fast-model",
	}, makeChatFunc(&calls, map[string][]runner.Event{
		decisionSessionID: {{Text: `{"action":"run","reason":"do it"}`}},
		mainSessionID:     {{Text: "Done."}},
	}), notifier)

	err := svc.Poll(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if err.Error() != "notify heartbeat result: notify failed" {
		t.Fatalf("unexpected error: %v", err)
	}
}
