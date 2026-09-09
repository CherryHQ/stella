package runtime

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/authz"
	agentaccess "github.com/CherryHQ/stella/internal/core/access"
	"github.com/CherryHQ/stella/pkg/plugins"
)

type preparationFailureRunner struct {
	chatFakeRunner
	prepareCalls *int
	prepareErr   *error
}

func (r *preparationFailureRunner) PrepareTurn(ctx context.Context, pluginContext PluginContext) (context.Context, PluginContext, error) {
	*r.prepareCalls++
	if *r.prepareErr != nil {
		err := *r.prepareErr
		*r.prepareErr = nil
		return nil, PluginContext{}, err
	}
	return ctx, pluginContext, nil
}

func TestPrepareChatAdmissionBuildsFreshContextOncePerTurn(t *testing.T) {
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
		return &chatFakeRunner{pluginContext: params.PluginContext}, nil
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
	if freshCalls != 1 {
		t.Fatalf("fresh builder calls = %d, want one capture per admission", freshCalls)
	}
	if len(built) != 1 || built[0].SessionPluginView().RegisteredPluginIDs[0] != "v1" {
		t.Fatalf("built contexts = %#v, want the single fresh capture", built)
	}
}

func TestPrepareChatAdmissionDerivesScopedAuthorityForEachBackgroundTurn(t *testing.T) {
	human, err := authz.NewUserAuthority(authz.UserID("owner"), true)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := agentaccess.WorkerAgentAuthority("owner", "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	var (
		builds int
		calls  []authz.Authority
	)
	rt, err := New(Config{
		Memory: fakeMemory{},
		NewRunner: func(context.Context, RunnerParams) (Runner, error) {
			builds++
			return &chatFakeRunner{}, nil
		},
		PluginContextBuilder: func(ctx context.Context, authority authz.Authority, _ string) (PluginContext, error) {
			fromContext, ok := authz.AuthorityFromContext(ctx)
			if !ok || fromContext != authority {
				return PluginContext{}, errors.New("plugin context received a different authority")
			}
			calls = append(calls, authority)
			return PluginContext{}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	info := session.Info{
		ID: "background-authority", UserID: "owner", AgentID: "agent-1",
		Kind: string(session.KindTask), Channel: string(session.ChannelScheduler),
	}
	inherited := authz.WithAuthority(context.Background(), human)
	for range 2 {
		admission, err := rt.BeginChatAdmission(inherited, info, "hello", nil, WithTurnAuthority(human))
		if err != nil {
			t.Fatal(err)
		}
		if err := rt.PrepareChatAdmission(admission); err != nil {
			t.Fatal(err)
		}
		rt.AbortChatAdmission(admission)
	}
	if builds != 1 {
		t.Fatalf("runner builds = %d, want one cached runner", builds)
	}
	if len(calls) != 2 {
		t.Fatalf("plugin context calls = %d, want one per turn", len(calls))
	}
	for _, got := range calls {
		if got != worker || got.Kind() != authz.ActorAgent || got.IsAdmin() {
			t.Fatalf("background authority = %#v, want confined worker %v", got, worker)
		}
	}
}

func TestPrepareChatAdmissionDerivesGroupAuthorityAndClearsInheritedUser(t *testing.T) {
	groupID := "00000000-0000-0000-0000-000000000701"
	want, err := agentaccess.GroupAgentAuthority(groupID, "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	human, err := authz.NewUserAuthority(authz.UserID("member"), true)
	if err != nil {
		t.Fatal(err)
	}
	var calls []authz.Authority
	rt, err := New(Config{
		Memory:    fakeMemory{},
		NewRunner: func(context.Context, RunnerParams) (Runner, error) { return &chatFakeRunner{}, nil },
		PluginContextBuilder: func(ctx context.Context, authority authz.Authority, _ string) (PluginContext, error) {
			fromContext, ok := authz.AuthorityFromContext(ctx)
			if !ok || fromContext != authority {
				return PluginContext{}, errors.New("group plugin context inherited the human authority")
			}
			calls = append(calls, authority)
			return PluginContext{}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	info := session.Info{
		ID: "group-authority", UserID: groupID, GroupID: groupID, AgentID: "agent-1",
		Kind: string(session.KindChat), Channel: "group:chat",
	}
	inherited := authz.WithAuthority(context.Background(), human)
	for range 2 {
		admission, err := rt.BeginChatAdmission(inherited, info, "hello", nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := rt.PrepareChatAdmission(admission); err != nil {
			t.Fatal(err)
		}
		rt.AbortChatAdmission(admission)
	}
	if len(calls) != 2 {
		t.Fatalf("plugin context calls = %d, want one per group turn", len(calls))
	}
	for _, got := range calls {
		if got != want || got.Kind() != authz.ActorGroupAgent || got.UserID() != "" {
			t.Fatalf("group authority = %#v, want confined group agent %v", got, want)
		}
	}
}

func TestPrepareChatAdmissionLeavesGuestsWithoutResourceAuthority(t *testing.T) {
	var calls, builds int
	rt, err := New(Config{
		Memory: fakeMemory{},
		NewRunner: func(context.Context, RunnerParams) (Runner, error) {
			builds++
			return &chatFakeRunner{}, nil
		},
		PluginContextBuilder: func(context.Context, authz.Authority, string) (PluginContext, error) {
			calls++
			return PluginContext{}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	guestID := "00000000-0000-0000-0000-000000000702"
	info := session.Info{ID: "guest-authority", UserID: guestID, GuestID: guestID, AgentID: "agent-1", Kind: string(session.KindChat), Channel: "guest"}
	admission, err := rt.BeginChatAdmission(t.Context(), info, "hello", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.PrepareChatAdmission(admission); err != nil {
		t.Fatal(err)
	}
	rt.AbortChatAdmission(admission)
	if calls != 0 || builds != 1 {
		t.Fatalf("guest plugin calls/builds = %d/%d, want 0/1", calls, builds)
	}
}

func TestChatInstallsDerivedAuthorityOnTurnContext(t *testing.T) {
	var captured context.Context
	rt, err := New(Config{
		Memory: fakeMemory{},
		NewRunner: func(_ context.Context, _ RunnerParams) (Runner, error) {
			return &chatFakeRunner{events: []Event{{Text: "ok"}}, ctx: &captured}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	want, err := agentaccess.WorkerAgentAuthority("owner", "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	human, err := authz.NewUserAuthority(authz.UserID("owner"), true)
	if err != nil {
		t.Fatal(err)
	}
	info := session.Info{
		ID: "background-chat-authority", UserID: "owner", AgentID: "agent-1",
		Kind: string(session.KindTask), Channel: string(session.ChannelScheduler),
	}
	for event := range rt.Chat(authz.WithAuthority(t.Context(), human), info, "hello") {
		if event.Err != nil {
			t.Fatal(event.Err)
		}
	}
	got, ok := authz.AuthorityFromContext(captured)
	if !ok || got != want {
		t.Fatalf("turn authority = %#v (present=%v), want confined worker %v", got, ok, want)
	}
}

func TestPrepareTurnFailureReleasesReservationWithoutRebuildingRunner(t *testing.T) {
	builds := 0
	prepareCalls := 0
	prepareFailure := errors.New("resource refresh failed")
	prepareErr := prepareFailure
	rt, err := New(Config{
		Memory: fakeMemory{},
		NewRunner: func(_ context.Context, params RunnerParams) (Runner, error) {
			builds++
			return &preparationFailureRunner{
				chatFakeRunner: chatFakeRunner{pluginContext: params.PluginContext},
				prepareCalls:   &prepareCalls,
				prepareErr:     &prepareErr,
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	info := session.Info{ID: "prepare-failure", UserID: "user", AgentID: "agent", Kind: string(session.KindChat), Channel: string(session.ChannelWeb)}
	first, err := rt.BeginChatAdmission(t.Context(), info, "hello", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.PrepareChatAdmission(first); err == nil || !errors.Is(err, prepareFailure) {
		t.Fatalf("first prepare error = %v, want resource refresh failure", err)
	}

	second, err := rt.BeginChatAdmission(t.Context(), info, "hello", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.PrepareChatAdmission(second); err != nil {
		t.Fatalf("second prepare: %v", err)
	}
	rt.AbortChatAdmission(second)
	if builds != 1 || prepareCalls != 2 {
		t.Fatalf("builds/prepares = %d/%d, want one build and two preparations", builds, prepareCalls)
	}
}

func TestDetachRunnersWhereCancelsPreCacheAdmission(t *testing.T) {
	started := make(chan struct{})
	rt, err := New(Config{
		Memory:    fakeMemory{},
		NewRunner: func(context.Context, RunnerParams) (Runner, error) { return &chatFakeRunner{}, nil },
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
