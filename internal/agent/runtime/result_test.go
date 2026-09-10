package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/agent/session"
)

type closeFailingChatRunner struct {
	Runner
	closeErr error
}

func (r *closeFailingChatRunner) Close() error { return r.closeErr }

func TestChatCombinesTurnAndCleanupResults(t *testing.T) {
	modelErr := errors.New("provider failed")
	closeErr := errors.New("runner close failed")
	for _, tc := range []struct {
		name       string
		modelErr   error
		closeErr   error
		panic      bool
		wantErrors []error
		wantResult string
	}{
		{"close failure", nil, closeErr, false, []error{closeErr}, "completed:error"},
		{"turn and close failures", modelErr, closeErr, false, []error{modelErr, closeErr}, "completed:error"},
		{"panic and close failure", nil, closeErr, true, []error{closeErr}, "completed:error"},
		{"timeout and close failure", ErrChatTimeout, closeErr, false, []error{closeErr}, "completed:error"},
		{"timeout notice", ErrChatTimeout, nil, false, nil, "completed:success"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mem := &activityRecordingMemory{}
			chat := &chatFakeRunner{events: []Event{{Text: "partial"}}}
			if tc.modelErr != nil {
				chat.events = append(chat.events, Event{Err: tc.modelErr})
			}
			runner := &closeFailingChatRunner{Runner: chat, closeErr: tc.closeErr}
			if tc.panic {
				runner.Runner = panicRunner{}
			}
			rt, err := New(Config{LocalOnly: true, Memory: mem, NewRunner: func(context.Context, RunnerParams) (Runner, error) { return runner, nil }})
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				runner.closeErr = nil
				_ = rt.Close()
			}()
			info := session.Info{ID: "result", UserID: "user", AgentID: "agent"}
			var got error
			var text strings.Builder
			failures := 0
			for event := range rt.Chat(t.Context(), info, "hello", WithOneShotRunner()) {
				text.WriteString(event.Text)
				if event.Err != nil {
					got = event.Err
					failures++
				}
			}
			wantFailures := min(1, len(tc.wantErrors))
			if failures != wantFailures {
				t.Fatalf("terminal errors = %d, want %d: %v", failures, wantFailures, got)
			}
			for _, want := range tc.wantErrors {
				if !errors.Is(got, want) {
					t.Fatalf("terminal error = %v, missing %v", got, want)
				}
			}
			if tc.panic && (got == nil || !strings.Contains(got.Error(), "chat turn panicked")) {
				t.Fatalf("cleanup hid panic: %v", got)
			}
			if errors.Is(tc.modelErr, ErrChatTimeout) && (!strings.Contains(text.String(), "reached the time limit") || errors.Is(got, ErrChatTimeout)) {
				t.Fatalf("timeout presentation changed: text=%q error=%v", text.String(), got)
			}
			if activity := mem.activitySnapshot(); len(activity) != 2 || activity[1] != tc.wantResult {
				t.Fatalf("activity = %v, want %s", activity, tc.wantResult)
			}
		})
	}
}
