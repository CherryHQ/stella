package runtime

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/skill"
	"github.com/CherryHQ/stella/pkg/plugins"
)

func TestPrepareChatAdmissionRefreshesFreshContextAfterPluginMutation(t *testing.T) {
	v1 := PluginContext{view: plugins.SessionPluginView{RegisteredPluginIDs: []string{"v1"}}}
	v2 := PluginContext{view: plugins.SessionPluginView{RegisteredPluginIDs: []string{"v2"}}}
	freshStarted := make(chan struct{})
	releaseFresh := make(chan struct{})
	release := sync.OnceFunc(func() { close(releaseFresh) })
	defer release()
	var (
		mu         sync.Mutex
		freshCalls int
		built      []PluginContext
	)

	factory := func(_ context.Context, params RunnerParams) (Runner, error) {
		mu.Lock()
		built = append(built, params.PluginContext)
		mu.Unlock()
		return chatFakeRunner{pluginContext: params.PluginContext}, nil
	}
	rt, err := New(Config{
		Memory:    fakeMemory{},
		NewRunner: factory,
		PluginContextBuilder: func(context.Context, authz.Authority, string) (PluginContext, error) {
			mu.Lock()
			freshCalls++
			call := freshCalls
			mu.Unlock()
			if call == 1 {
				close(freshStarted)
				<-releaseFresh
				return v1, nil
			}
			return v2, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	authority, err := authz.NewUserAuthority(authz.UserID("u1"), false)
	if err != nil {
		t.Fatal(err)
	}
	info := session.NewInfo("fresh-generation", "agent1", "u1", "web", session.KindChat, "", time.Now().UTC())
	admission, err := rt.BeginChatAdmission(context.Background(), info, "hello", nil, WithTurnAuthority(authority))
	if err != nil {
		t.Fatal(err)
	}
	prepared := make(chan error, 1)
	go func() { prepared <- rt.PrepareChatAdmission(admission) }()
	<-freshStarted
	if err := rt.DetachRunnersForMutation()(); err != nil {
		t.Fatal(err)
	}
	release()
	if err := <-prepared; err != nil {
		t.Fatalf("prepare after plugin mutation: %v", err)
	}
	defer rt.AbortChatAdmission(admission)

	mu.Lock()
	defer mu.Unlock()
	if freshCalls < 2 {
		t.Fatalf("fresh builder calls = %d, want refresh after mutation", freshCalls)
	}
	if len(built) != 1 || !built[0].SameIdentity(v2) {
		t.Fatalf("built contexts = %#v, want only v2", built)
	}
}

func TestPrepareChatAdmissionRetriesSkillRevisionRegistration(t *testing.T) {
	owner := &skill.ActiveTurnOwner{}
	var captures int
	rt, err := New(Config{
		Memory:         fakeMemory{},
		NewRunner:      func(context.Context, RunnerParams) (Runner, error) { return chatFakeRunner{}, nil },
		SkillTurnOwner: owner,
		SkillTurnCapture: func(ctx context.Context, _ session.Info, _ PluginContext) (context.Context, error) {
			captures++
			view, err := skill.NewSkillTurnView(nil, nil, nil, nil)
			if err != nil {
				return nil, err
			}
			return skill.WithSkillTurnView(ctx, view), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	registers := 0
	rt.skillTurnRegistrar = func(ctx context.Context, turnID string, view skill.SkillTurnView, register func(string, skill.SkillTurnView) error) error {
		registers++
		if registers == 1 {
			if err := register(turnID, view); err != nil {
				return err
			}
			return errors.Join(skill.ErrSkillTurnRevisionChanged, errors.New("published concurrently"))
		}
		return register(turnID, view)
	}
	info := session.Info{ID: "skill-retry", UserID: "user", AgentID: "agent", Kind: string(session.KindChat), Channel: string(session.ChannelWeb)}
	admission, err := rt.BeginChatAdmission(t.Context(), info, "hello", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.PrepareChatAdmission(admission); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	defer rt.AbortChatAdmission(admission)
	if captures != 2 || registers != 2 {
		t.Fatalf("capture/register calls = %d/%d, want 2/2", captures, registers)
	}
	if got := len(owner.Snapshot()); got != 1 {
		t.Fatalf("active skill owners = %d, want 1", got)
	}
}

func TestDetachRunnersWhereCancelsPreCacheAdmission(t *testing.T) {
	started := make(chan struct{})
	rt, err := New(Config{
		Memory:    fakeMemory{},
		NewRunner: func(context.Context, RunnerParams) (Runner, error) { return chatFakeRunner{}, nil },
		PluginContextBuilder: func(ctx context.Context, _ authz.Authority, _ string) (PluginContext, error) {
			close(started)
			<-ctx.Done()
			return PluginContext{}, ctx.Err()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	authority, err := authz.NewUserAuthority(authz.UserID("u1"), false)
	if err != nil {
		t.Fatal(err)
	}
	info := session.Info{ID: "pre-cache", UserID: "u1", AgentID: "agent1", Kind: string(session.KindChat), Channel: string(session.ChannelWeb)}
	admission, err := rt.BeginChatAdmission(t.Context(), info, "hello", nil, WithTurnAuthority(authority))
	if err != nil {
		t.Fatal(err)
	}
	prepared := make(chan error, 1)
	go func() { prepared <- rt.PrepareChatAdmission(admission) }()
	<-started
	closeDetached := rt.DetachRunnersWhere(func(got session.Info) bool { return got.UserID == "u1" })
	if err := closeDetached(); err != nil {
		t.Fatal(err)
	}
	if err := <-prepared; !errors.Is(err, context.Canceled) {
		t.Fatalf("prepare after terminal detach = %v, want context canceled", err)
	}
}
