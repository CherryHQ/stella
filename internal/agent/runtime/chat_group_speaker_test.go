package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/authz"
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
			gotCtxUserIDs = append(gotCtxUserIDs, authz.UserIDFromContext(ctx))
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
			t.Errorf("turn %d: authz.UserIDFromContext = %q, want empty (group D9)", i, uid)
		}
	}
}

func TestRuntimeChatGroupSpeakerContextInjectedIntoModelMessageOnly(t *testing.T) {
	mem := &recordingMemory{}
	var modelMessages []MessageContent

	rt, err := New(Config{
		Memory: mem,
		NewRunner: func(context.Context, RunnerParams) (Runner, error) {
			return chatFakeRunner{events: []Event{{Text: "ok"}}, messages: &modelMessages}, nil
		},
	})
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}

	info := session.Info{ID: "sess-1", UserID: "group-1", AgentID: "agent-1", GroupID: "group-1"}
	out := make(chan Event, 10)
	rt.chat(
		memory.WithGroupSeq(context.Background(), 1),
		out, info, "hi Stella",
		chatOptions{currentSpeaker: memory.CurrentSpeaker{UserID: "alice", DisplayName: "Alice"}, hasSpeaker: true},
	)
	for range out { //nolint:revive // drain
	}

	if len(modelMessages) != 1 {
		t.Fatalf("model messages = %d, want 1", len(modelMessages))
	}
	modelMsg, ok := modelMessages[0].(ai.UserMessage)
	if !ok {
		t.Fatalf("model message type = %T, want ai.UserMessage", modelMessages[0])
	}
	modelText := flattenRuntimeUserMessage(modelMsg)
	for _, want := range []string{"<current_speaker>", "Name: Alice", "Linked Stella user: yes", "hi Stella"} {
		if !strings.Contains(modelText, want) {
			t.Fatalf("model message missing %q:\n%s", want, modelText)
		}
	}

	if len(mem.messages) != 2 {
		t.Fatalf("stored messages = %d, want user + assistant", len(mem.messages))
	}
	storedUser, ok := mem.messages[0].(ai.UserMessage)
	if !ok {
		t.Fatalf("stored first message = %T, want ai.UserMessage", mem.messages[0])
	}
	if storedUser.Content != "hi Stella" {
		t.Fatalf("stored user content = %#v, want original message", storedUser.Content)
	}
}
