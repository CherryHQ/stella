package runtime

import (
	"context"
	"testing"

	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/memory"
)

// A reused group runner must rebuild the system prompt per turn with the current
// speaker, and must never promote the speaker to the runtime/session user (D9).
func TestRuntimeChatGroupPromptPerSpeakerNoContamination(t *testing.T) {
	mem := &recordingMemory{}
	var gotSpeakers []string
	var gotCtxUserIDs []string

	rt, err := New(Config{
		Memory: mem,
		NewRunner: func(context.Context, RunnerParams) (Runner, error) {
			return chatFakeRunner{events: []Event{{Text: "ok"}}}, nil
		},
		GroupPrompt: func(ctx context.Context, _ session.Info, speaker memory.CurrentSpeaker) string {
			gotSpeakers = append(gotSpeakers, speaker.UserID)
			if cs, ok := memory.CurrentSpeakerFromContext(ctx); !ok || cs.UserID != speaker.UserID {
				t.Errorf("ctx speaker = %+v ok=%v, want UserID %q", cs, ok, speaker.UserID)
			}
			gotCtxUserIDs = append(gotCtxUserIDs, memory.UserIDFromContext(ctx))
			return "system-for-" + speaker.UserID
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

	// Per-turn rebuild: one call per turn, each with its own speaker.
	if len(gotSpeakers) != 2 || gotSpeakers[0] != "alice" || gotSpeakers[1] != "bob" {
		t.Fatalf("group prompt speakers = %v, want [alice bob]", gotSpeakers)
	}
	// D9: the memory user id is never set to a human in a group turn.
	for i, uid := range gotCtxUserIDs {
		if uid != "" {
			t.Errorf("turn %d: memory.UserIDFromContext = %q, want empty (group D9)", i, uid)
		}
	}
}
