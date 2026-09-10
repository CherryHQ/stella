package agent

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agentruntime "github.com/CherryHQ/stella/internal/agent/runtime"
	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/memory/memorytest"
	"github.com/CherryHQ/stella/internal/platform/home"
	"github.com/CherryHQ/stella/internal/plugin"
	"github.com/CherryHQ/stella/pkg/ai"
)

type revocationNetworkError struct{}

func (revocationNetworkError) Error() string   { return "connection reset" }
func (revocationNetworkError) Timeout() bool   { return true }
func (revocationNetworkError) Temporary() bool { return true }

func TestUserRevocationOutcomeUnknownConservativeTransportClassification(t *testing.T) {
	knownRollback := errors.New("constraint violation")
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{name: "commit marker", err: plugin.ErrCommitOutcomeUnknown, want: true},
		{name: "home marker", err: home.ErrOutcomeUnknown, want: true},
		{name: "deadline", err: context.DeadlineExceeded, want: true},
		{name: "canceled", err: context.Canceled, want: true},
		{name: "eof", err: io.EOF, want: true},
		{name: "unexpected eof", err: io.ErrUnexpectedEOF, want: true},
		{name: "network", err: revocationNetworkError{}, want: true},
		{name: "wrapped network", err: errors.Join(errors.New("vault write"), revocationNetworkError{}), want: true},
		{name: "known rollback", err: knownRollback, want: false},
		{name: "nil", err: nil, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := userRevocationOutcomeUnknown(tc.err); got != tc.want {
				t.Fatalf("unknown(%v) = %t, want %t", tc.err, got, tc.want)
			}
		})
	}
}

type revocationBlockingRunner struct {
	closeStarted chan struct{}
	releaseClose chan struct{}
	closeOnce    sync.Once
	busy         atomic.Bool
}

func (r *revocationBlockingRunner) Chat(ctx context.Context, _ []ai.Message, _ agentruntime.MessageContent) <-chan agentruntime.Event {
	r.busy.Store(true)
	out := make(chan agentruntime.Event)
	go func() {
		defer close(out)
		defer r.busy.Store(false)
		<-ctx.Done()
	}()
	return out
}
func (*revocationBlockingRunner) Alive() bool             { return true }
func (r *revocationBlockingRunner) Busy() bool            { return r.busy.Load() }
func (*revocationBlockingRunner) LastActivity() time.Time { return time.Now() }
func (*revocationBlockingRunner) SystemPrompt() string    { return "" }
func (*revocationBlockingRunner) PluginContext() PluginContext {
	return PluginContext{}
}

func (r *revocationBlockingRunner) Close() error {
	r.closeOnce.Do(func() { close(r.closeStarted) })
	<-r.releaseClose
	return nil
}

func TestApplyUserRevocationCutsOffMatchingTurnsBeforeSlowClose(t *testing.T) {
	pm := NewPoolManager(nil, memorytest.New(), WithLocalExecution())
	var runners []*revocationBlockingRunner
	rt, err := agentruntime.New(agentruntime.Config{
		LocalOnly: true,
		Memory:    memorytest.New(),
		NewRunner: func(context.Context, agentruntime.RunnerParams) (agentruntime.Runner, error) {
			runner := &revocationBlockingRunner{closeStarted: make(chan struct{}), releaseClose: make(chan struct{})}
			runners = append(runners, runner)
			return runner, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{Runtime: rt, AgentID: "agent", lifecycle: pm.lifecycle}
	pm.services[svc.AgentID] = svc
	for i := range 2 {
		info := session.Info{ID: "session-" + string(rune('a'+i)), UserID: "user", AgentID: "agent", Kind: string(session.KindChat), Channel: string(session.ChannelWeb)}
		stream, admitErr := svc.admit(t.Context(), info, "hello")
		if admitErr != nil {
			t.Fatal(admitErr)
		}
		_ = stream
	}
	if len(runners) != 2 {
		t.Fatalf("built runners = %d, want 2", len(runners))
	}
	done := make(chan error, 1)
	go func() { done <- pm.ApplyUserRevocation(t.Context(), "user", "", func() error { return nil }) }()
	// Cache map iteration does not order the two slow closes.
	select {
	case <-runners[0].closeStarted:
	case <-runners[1].closeStarted:
	case <-time.After(time.Second):
		t.Fatal("revocation did not begin slow runner close")
	}
	// Detach cancels every matching active turn before the first slow Close.
	select {
	case <-runners[0].releaseClose:
	default:
	}
	closeState := func() bool {
		for _, runner := range runners {
			if runner.Busy() {
				return true
			}
		}
		return false
	}
	deadline := time.After(time.Second)
	for closeState() {
		select {
		case <-deadline:
			t.Fatal("matching active turns were not canceled")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	if err := pm.AdmitToolCall(context.Background()); err != nil {
		t.Fatalf("unrelated tool admission blocked by slow close: %v", err)
	}
	close(runners[0].releaseClose)
	close(runners[1].releaseClose)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestApplyUserRevocationRollbackKeepsTurnsAndUnknownCutsOff(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result error
		cutoff bool
	}{
		{name: "known rollback", result: errors.New("constraint"), cutoff: false},
		{name: "transport unknown", result: io.EOF, cutoff: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pm := NewPoolManager(nil, memorytest.New(), WithLocalExecution())
			runner := &revocationBlockingRunner{closeStarted: make(chan struct{}), releaseClose: make(chan struct{})}
			rt, err := agentruntime.New(agentruntime.Config{LocalOnly: true, Memory: memorytest.New(), NewRunner: func(context.Context, agentruntime.RunnerParams) (agentruntime.Runner, error) {
				return runner, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			svc := &Service{Runtime: rt, AgentID: "agent", lifecycle: pm.lifecycle}
			pm.services[svc.AgentID] = svc
			stream, err := svc.admit(t.Context(), session.Info{ID: "session", UserID: "user", AgentID: "agent", Kind: string(session.KindChat), Channel: string(session.ChannelWeb)}, "hello")
			if err != nil {
				t.Fatal(err)
			}
			_ = stream
			deadline := time.After(time.Second)
			for !runner.Busy() {
				select {
				case <-deadline:
					t.Fatal("test runner did not become busy")
				default:
					time.Sleep(time.Millisecond)
				}
			}
			if tc.cutoff {
				done := make(chan error, 1)
				go func() { done <- pm.ApplyUserRevocation(t.Context(), "user", "", func() error { return tc.result }) }()
				select {
				case <-runner.closeStarted:
				case <-time.After(time.Second):
					t.Fatal("unknown revocation did not start close")
				}
				deadline := time.After(time.Second)
				for runner.Busy() {
					select {
					case <-deadline:
						t.Fatal("unknown revocation left active turn running")
					default:
						time.Sleep(time.Millisecond)
					}
				}
				close(runner.releaseClose)
				if err := <-done; !errors.Is(err, tc.result) {
					t.Fatalf("revocation result = %v, want %v", err, tc.result)
				}
			} else {
				if err := pm.ApplyUserRevocation(t.Context(), "user", "", func() error { return tc.result }); !errors.Is(err, tc.result) {
					t.Fatalf("revocation result = %v, want %v", err, tc.result)
				}
				if !runner.Busy() {
					t.Fatal("known rollback canceled active turn")
				}
				close(runner.releaseClose)
				_ = rt.Close()
			}
		})
	}
}

func TestApplyUserRevocationEmptyIdentifiersSelectDeploymentOrAgent(t *testing.T) {
	testScope := func(t *testing.T, userID, agentID string, wantClose map[string]bool) {
		t.Helper()
		pm := NewPoolManager(nil, memorytest.New(), WithLocalExecution())
		runners := make(map[string]*revocationBlockingRunner)
		runtimes := make([]*agentruntime.Runtime, 0, 2)
		for _, owner := range []struct {
			agent string
			user  string
		}{
			{agent: "agent-a", user: "user-a"},
			{agent: "agent-b", user: "user-b"},
		} {
			runner := &revocationBlockingRunner{closeStarted: make(chan struct{}), releaseClose: make(chan struct{})}
			runners[owner.agent] = runner
			rt, err := agentruntime.New(agentruntime.Config{
				LocalOnly: true,
				Memory:    memorytest.New(),
				NewRunner: func(context.Context, agentruntime.RunnerParams) (agentruntime.Runner, error) {
					return runner, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			runtimes = append(runtimes, rt)
			svc := &Service{Runtime: rt, AgentID: owner.agent, lifecycle: pm.lifecycle}
			pm.services[owner.agent] = svc
			stream, err := svc.admit(t.Context(), session.Info{
				ID: "session-" + owner.agent, UserID: owner.user, AgentID: owner.agent,
				Kind: string(session.KindChat), Channel: string(session.ChannelWeb),
			}, "hello")
			if err != nil {
				t.Fatal(err)
			}
			_ = stream
		}

		done := make(chan error, 1)
		go func() { done <- pm.ApplyUserRevocation(t.Context(), userID, agentID, func() error { return nil }) }()
		for agent, want := range wantClose {
			if want {
				// Release up front so a slow close on one Service cannot prevent
				// the coordinator from proving the other matching Service was
				// detached too. Cancellation happened synchronously under the
				// lifecycle gate before either close starts.
				close(runners[agent].releaseClose)
			}
		}
		for agent, want := range wantClose {
			if want {
				select {
				case <-runners[agent].closeStarted:
				case <-time.After(time.Second):
					t.Fatalf("%s runner was not selected for revocation", agent)
				}
			} else {
				select {
				case <-runners[agent].closeStarted:
					t.Fatalf("%s runner was selected for revocation", agent)
				case <-time.After(20 * time.Millisecond):
				}
				if !runners[agent].Busy() {
					t.Fatalf("%s active turn was canceled", agent)
				}
			}
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		for agent, want := range wantClose {
			if !want {
				close(runners[agent].releaseClose)
			}
		}
		for _, rt := range runtimes {
			if err := rt.Close(); err != nil {
				t.Fatal(err)
			}
		}
	}

	t.Run("whole deployment", func(t *testing.T) {
		testScope(t, "", "", map[string]bool{"agent-a": true, "agent-b": true})
	})
	t.Run("all users on one agent", func(t *testing.T) {
		testScope(t, "", "agent-a", map[string]bool{"agent-a": true, "agent-b": false})
	})
}
