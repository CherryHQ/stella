package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/pkg/ai"
)

type panicRunner struct{}

func (panicRunner) Chat(_ context.Context, _ []ai.Message, _ MessageContent) <-chan Event {
	panic("boom")
}
func (panicRunner) Alive() bool             { return true }
func (panicRunner) Busy() bool              { return false }
func (panicRunner) LastActivity() time.Time { return time.Now() }
func (panicRunner) SystemPrompt() string    { return "" }
func (panicRunner) Close() error            { return nil }

// A panic inside the turn must not wedge the session: the caller's channel
// still closes, the busy guard clears, and the hub stops reporting the session
// as live (otherwise SSE watchers would poll a never-closing stream forever).
func TestChat_PanicRecovers_FreesSessionAndHub(t *testing.T) {
	mem := &recordingMemory{}
	rt, _ := New(Config{
		NewRunner: func(_ context.Context, _ RunnerParams) (Runner, error) {
			return panicRunner{}, nil
		},
		Memory: mem,
	})

	info := session.Info{ID: "sess-1", UserID: "u1", AgentID: "a1"}
	ch := rt.Chat(context.Background(), info, "hi")
	for range ch { //nolint:revive // drain until the forwarder closes out
	}

	waitSessionFree(t, rt, info.ID)
	if rt.SessionLive(info.ID) {
		t.Fatal("session stuck live after a panicking turn")
	}
}
