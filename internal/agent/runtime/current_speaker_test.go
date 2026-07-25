package runtime

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/ai"
)

type recordingChatRunner struct {
	system  string
	events  []Event
	history []ai.Message
	message MessageContent
}

func (r *recordingChatRunner) Chat(_ context.Context, history []ai.Message, message MessageContent) <-chan Event {
	r.history = append([]ai.Message(nil), history...)
	r.message = message
	ch := make(chan Event, len(r.events))
	for _, evt := range r.events {
		ch <- evt
	}
	close(ch)
	return ch
}

func (r *recordingChatRunner) Alive() bool             { return true }
func (r *recordingChatRunner) Busy() bool              { return false }
func (r *recordingChatRunner) LastActivity() time.Time { return time.Now() }
func (r *recordingChatRunner) SystemPrompt() string    { return r.system }
func (r *recordingChatRunner) Close() error            { return nil }

func TestRuntimeChatInjectsCurrentSpeakerIntoGroupTurnMessage(t *testing.T) {
	mem := &recordingMemory{}
	runner := &recordingChatRunner{system: "stable group system", events: []Event{{Text: "ok"}}}
	var beforeRunSystems []string

	rt, err := New(Config{
		Memory: mem,
		NewRunner: func(context.Context, RunnerParams) (Runner, error) {
			return runner, nil
		},
		BeforeRun: func(_ context.Context, _ session.Info, _, _, system string, _ []ai.Message) (string, error) {
			beforeRunSystems = append(beforeRunSystems, system)
			return system, nil
		},
	})
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}

	info := session.Info{ID: "sess-1", UserID: "11111111-1111-4111-8111-111111111111", AgentID: "agent-1", GroupID: "11111111-1111-4111-8111-111111111111"}
	out := make(chan Event, 10)
	rt.chat(
		memory.WithGroupSeq(context.Background(), 7),
		out,
		info,
		"hello everyone",
		chatOptions{currentSpeaker: memory.CurrentSpeaker{DisplayName: "Alice", UserID: "alice-user"}, hasSpeaker: true},
	)
	for range out { // drain
	}

	if len(beforeRunSystems) != 1 || beforeRunSystems[0] != "stable group system" {
		t.Fatalf("before-run systems = %q, want stable base system", beforeRunSystems)
	}
	got := flattenRuntimeUserMessage(runner.message.(ai.UserMessage))
	for _, want := range []string{"<current_speaker>", "Name: Alice", "Linked Stella user: yes", "hello everyone"} {
		if !strings.Contains(got, want) {
			t.Fatalf("model message missing %q:\n%s", want, got)
		}
	}
	if len(mem.messages) == 0 {
		t.Fatal("expected persisted group user message")
	}
	if persisted := flattenRuntimeUserMessage(mem.messages[0]); persisted != "hello everyone" {
		t.Fatalf("persisted group user message = %q, want original user text only", persisted)
	}
}

func TestWithCurrentSpeakerContextSupportsMultimodalContent(t *testing.T) {
	msg := []ai.ContentBlock{
		ai.ImageContent{Data: "abc", MimeType: "image/png"},
		ai.TextContent{Text: "what is this?"},
	}
	got, ok := withCurrentSpeakerContext(msg, memory.CurrentSpeaker{DisplayName: "Bob"}, true).([]ai.ContentBlock)
	if !ok {
		t.Fatalf("speaker context content = %T, want []ai.ContentBlock", got)
	}
	if len(got) != 3 {
		t.Fatalf("blocks = %d, want speaker prefix + original 2 blocks", len(got))
	}
	prefix, ok := got[0].(ai.TextContent)
	if !ok || !strings.Contains(prefix.Text, "Name: Bob") || !strings.Contains(prefix.Text, "Linked Stella user: no") {
		t.Fatalf("prefix block = %#v", got[0])
	}
	if got[1] != msg[0] || got[2] != msg[1] {
		t.Fatalf("original blocks not preserved: %#v", got)
	}
}

func TestStructuredGroupTurnOmitsPrivateSpeakerProfileAffordance(t *testing.T) {
	mem := &recordingMemory{}
	runner := &recordingChatRunner{events: []Event{{Text: "ok"}}}
	var contextSpeaker memory.CurrentSpeaker
	rt, err := New(Config{
		Memory:                mem,
		StructuredGroupMemory: true,
		NewRunner: func(context.Context, RunnerParams) (Runner, error) {
			return runner, nil
		},
		BeforeRun: func(ctx context.Context, _ session.Info, _, _, system string, _ []ai.Message) (string, error) {
			contextSpeaker, _ = memory.CurrentSpeakerFromContext(ctx)
			return system, nil
		},
	})
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}

	out := make(chan Event, 10)
	rt.chat(
		memory.WithGroupSeq(context.Background(), 7),
		out,
		session.Info{
			ID:      "sess-1",
			UserID:  "11111111-1111-4111-8111-111111111111",
			AgentID: "agent-1",
			GroupID: "11111111-1111-4111-8111-111111111111",
		},
		"hello",
		chatOptions{currentSpeaker: memory.CurrentSpeaker{DisplayName: "Alice", UserID: "private-user"}, hasSpeaker: true},
	)
	for range out {
	}

	got := flattenRuntimeUserMessage(runner.message.(ai.UserMessage))
	if !strings.Contains(got, "Name: Alice") {
		t.Fatalf("structured speaker context missing public display name:\n%s", got)
	}
	for _, forbidden := range []string{"Linked Stella user", "profile available", "memory.profile_get"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("structured speaker context leaked legacy affordance %q:\n%s", forbidden, got)
		}
	}
	if contextSpeaker.UserID != "" || contextSpeaker.DisplayName != "Alice" {
		t.Fatalf("structured context speaker = %#v, want public display name without private user link", contextSpeaker)
	}
}
