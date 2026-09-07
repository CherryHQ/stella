package runtime

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/authz"
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
