package runtime

import (
	"context"
	"testing"

	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/ai"
)

// A reused group runner must expose the current speaker to per-turn hooks and
// must never promote the speaker to the runtime/session user (D9).
func TestRuntimeChatGroupSpeakerContextNoUserPromotion(t *testing.T) {
	mem := &recordingMemory{}
	var gotSpeakers []string
	var gotCtxUserIDs []string

	rt, err := New(Config{
		Memory: mem,
		NewRunner: func(context.Context, RunnerParams) (Runner, error) {
			return chatFakeRunner{events: []Event{{Text: "ok"}}}, nil
		},
		BeforeRun: func(ctx context.Context, _ session.Info, _, _, system string, _ []ai.Message) (string, error) {
			cs, ok := memory.CurrentSpeakerFromContext(ctx)
			if !ok {
				t.Fatal("missing current speaker in group turn context")
			}
			gotSpeakers = append(gotSpeakers, cs.UserID)
			gotCtxUserIDs = append(gotCtxUserIDs, memory.UserIDFromContext(ctx))
			return system, nil
		},
	})
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}

	info := session.Info{ID: "sess-1", UserID: "group-1", AgentID: "agent-1", GroupID: "group-1"}
	for _, speaker := range []string{"alice", "bob"} {
		out := make(chan Event, 10)
		rt.chat(
			memory.WithGroupSeq(context.Background(), 1),
			out, info, "hi",
			chatOptions{currentSpeaker: memory.CurrentSpeaker{UserID: speaker}, hasSpeaker: true},
		)
		for range out { //nolint:revive // drain
		}
	}

	// Per-turn context: one hook call per turn, each with its own speaker.
	if len(gotSpeakers) != 2 || gotSpeakers[0] != "alice" || gotSpeakers[1] != "bob" {
		t.Fatalf("current speakers = %v, want [alice bob]", gotSpeakers)
	}
	// D9: the memory user id is never set to a human in a group turn.
	for i, uid := range gotCtxUserIDs {
		if uid != "" {
			t.Errorf("turn %d: memory.UserIDFromContext = %q, want empty (group D9)", i, uid)
		}
	}
}
