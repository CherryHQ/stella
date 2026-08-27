package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/ai"
)

type authorityCarrierRunner struct{ contexts *[]context.Context }

func (r *authorityCarrierRunner) Chat(ctx context.Context, _ []ai.Message, _ MessageContent) <-chan Event {
	*r.contexts = append(*r.contexts, ctx)
	out := make(chan Event, 1)
	out <- Event{Text: "ok"}
	close(out)
	return out
}
func (*authorityCarrierRunner) Alive() bool             { return true }
func (*authorityCarrierRunner) Busy() bool              { return false }
func (*authorityCarrierRunner) LastActivity() time.Time { return time.Now() }
func (*authorityCarrierRunner) SystemPrompt() string    { return "" }
func (*authorityCarrierRunner) Close() error            { return nil }

func TestAuthorityCarrierDoesNotStickToCachedRunner(t *testing.T) {
	mem := &recordingMemory{}
	var contexts []context.Context
	rt, err := New(Config{Memory: mem, NewRunner: func(context.Context, RunnerParams) (Runner, error) {
		return &authorityCarrierRunner{contexts: &contexts}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	admin, err := authz.NewUserAuthority("admin", true)
	if err != nil {
		t.Fatal(err)
	}
	ordinary, err := authz.NewUserAuthority("ordinary", false)
	if err != nil {
		t.Fatal(err)
	}
	parent := authz.WithAuthority(context.Background(), admin)
	info := session.Info{ID: "stella-session", UserID: "ordinary", AgentID: "stella", Kind: string(session.KindChat)}
	first, err := rt.ChatAdmitted(parent, info, "first", WithTurnAuthority(ordinary))
	if err != nil {
		t.Fatal(err)
	}
	for range first {
	}
	second, err := rt.ChatAdmitted(parent, info, "second")
	if err != nil {
		t.Fatal(err)
	}
	for range second {
	}
	if len(contexts) != 2 {
		t.Fatalf("runner calls = %d, want 2", len(contexts))
	}
	if got, ok := authz.AuthorityFromContext(contexts[0]); !ok || got != ordinary {
		t.Fatalf("first carrier = %#v, %v", got, ok)
	}
	if _, ok := authz.AuthorityFromContext(contexts[1]); ok {
		t.Fatal("second turn inherited first/parent Authority")
	}
	_ = memory.SessionIDFromContext(contexts[0])
}
