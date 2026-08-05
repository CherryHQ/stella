package runtime

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/fsops"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/sandbox"
)

type filesystemSession struct {
	sandbox.Session
	root string
}

func (s filesystemSession) WorkingDir() string { return "/workspace" }
func (s filesystemSession) Filesystem() (sandbox.Filesystem, error) {
	return fsops.NewFilesystem([]fsops.Mount{{Path: sandbox.PathWorkspace, Directory: s.root}})
}

type panicFilesystemSession struct{ sandbox.Session }

func (panicFilesystemSession) WorkingDir() string                      { return "/workspace" }
func (panicFilesystemSession) Filesystem() (sandbox.Filesystem, error) { panic("provider boom") }

type sandboxRunner struct {
	sess   sandbox.Session
	closed bool
}

func (r *sandboxRunner) Chat(context.Context, []ai.Message, MessageContent) <-chan Event {
	ch := make(chan Event)
	close(ch)
	return ch
}
func (r *sandboxRunner) Alive() bool             { return r.sess.Alive() }
func (r *sandboxRunner) Busy() bool              { return false }
func (r *sandboxRunner) LastActivity() time.Time { return time.Now() }
func (r *sandboxRunner) SystemPrompt() string    { return "" }
func (r *sandboxRunner) SandboxSession() sandbox.Session {
	return r.sess
}

func (r *sandboxRunner) Close() error {
	r.closed = true
	return r.sess.Close()
}

type blockingSandboxRunner struct {
	*sandboxRunner
	chatStarted chan struct{}
	chatStream  chan Event
}

func (r *blockingSandboxRunner) Chat(context.Context, []ai.Message, MessageContent) <-chan Event {
	close(r.chatStarted)
	return r.chatStream
}

func TestCloseSessionWithSandboxInvokesCallbackBeforeClose(t *testing.T) {
	r := &sandboxRunner{sess: sandbox.NopSession()}
	cache := newRunnerCache(func(context.Context, RunnerParams) (Runner, error) { return r, nil }, fakeMemory{}, 10*time.Minute, slog.Default())
	info := session.NewInfo("s1", "agent1", "u1", "web", session.KindTask, "", time.Now().UTC())
	if _, _, err := cache.getOrCreate(context.Background(), info, "", ""); err != nil {
		t.Fatalf("getOrCreate: %v", err)
	}
	called := false
	if err := cache.closeWithSandbox("s1", func(sess sandbox.Session) error {
		called = true
		if sess == nil || !sess.Alive() {
			t.Fatalf("callback got closed sandbox")
		}
		if r.closed {
			t.Fatalf("runner closed before sandbox callback")
		}
		return nil
	}); err != nil {
		t.Fatalf("closeWithSandbox: %v", err)
	}
	if !called {
		t.Fatal("sandbox callback was not called")
	}
	if !r.closed || r.sess.Alive() {
		t.Fatalf("runner/sandbox not closed after callback")
	}
}

func TestCloseSessionWithSandboxClosesAfterCallbackError(t *testing.T) {
	want := errors.New("check failed")
	r := &sandboxRunner{sess: sandbox.NopSession()}
	cache := newRunnerCache(func(context.Context, RunnerParams) (Runner, error) { return r, nil }, fakeMemory{}, 10*time.Minute, slog.Default())
	info := session.NewInfo("s1", "agent1", "u1", "web", session.KindTask, "", time.Now().UTC())
	if _, _, err := cache.getOrCreate(context.Background(), info, "", ""); err != nil {
		t.Fatalf("getOrCreate: %v", err)
	}
	err := cache.closeWithSandbox("s1", func(sandbox.Session) error { return want })
	if !errors.Is(err, want) {
		t.Fatalf("closeWithSandbox err=%v want callback error", err)
	}
	if !r.closed || r.sess.Alive() {
		t.Fatalf("runner/sandbox not closed after callback error")
	}
}

func TestUseFilesystemReusesRunnerAndDefersReset(t *testing.T) {
	root := t.TempDir()
	r := &sandboxRunner{sess: filesystemSession{Session: sandbox.NopSession(), root: root}}
	created := 0
	rt, err := New(Config{NewRunner: func(context.Context, RunnerParams) (Runner, error) { created++; return r, nil }, Memory: fakeMemory{}})
	if err != nil {
		t.Fatal(err)
	}
	info := session.NewInfo("s1", "agent1", "u1", "web", session.KindTask, "", time.Now().UTC())
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- rt.UseFilesystem(context.Background(), info, func(sandbox.Filesystem) error { close(entered); <-release; return nil })
	}()
	<-entered
	if err := rt.ResetRunners(); err != nil {
		t.Fatal(err)
	}
	if r.closed {
		t.Fatal("reset closed a leased filesystem runner")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := rt.UseFilesystem(context.Background(), info, func(f sandbox.Filesystem) error {
		return f.Write(context.Background(), "/workspace/x", nil, sandbox.WriteOptions{})
	}); err == nil {
		t.Fatal("nil writer must fail")
	}
	if created != 2 {
		t.Fatalf("created=%d, want 2 after deferred reset", created)
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatal(err)
	}
}

func TestUseFilesystemConcurrentCallbacksHoldCountedLease(t *testing.T) {
	r := &sandboxRunner{sess: filesystemSession{Session: sandbox.NopSession(), root: t.TempDir()}}
	created := 0
	rt, err := New(Config{NewRunner: func(context.Context, RunnerParams) (Runner, error) { created++; return r, nil }, Memory: fakeMemory{}, IdleTimeout: time.Nanosecond})
	if err != nil {
		t.Fatal(err)
	}
	info := session.NewInfo("s1", "agent1", "u1", "web", session.KindTask, "", time.Now().UTC())
	// Warm the runner first so both concurrent callbacks reuse the one cached
	// runner; a cold concurrent start would race two independent constructions.
	if err := rt.UseFilesystem(context.Background(), info, func(sandbox.Filesystem) error { return nil }); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}, 2), make(chan struct{})
	done := make(chan error, 2)
	for range 2 {
		go func() {
			done <- rt.UseFilesystem(context.Background(), info, func(sandbox.Filesystem) error { entered <- struct{}{}; <-release; return nil })
		}()
	}
	<-entered
	<-entered
	rt.cache.reap()
	if r.closed {
		t.Fatal("reaper closed a concurrently leased runner")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if created != 1 {
		t.Fatalf("created=%d, want one warm runner", created)
	}
	if got := leasesFor(rt, info.ID); got != 0 {
		t.Fatalf("concurrent callbacks left leases=%d, want each to release its own", got)
	}
}

func TestUseFilesystemCallbackPanicReleasesLeaseAndKeepsRunner(t *testing.T) {
	root := t.TempDir()
	r := &sandboxRunner{sess: filesystemSession{Session: sandbox.NopSession(), root: root}}
	created := 0
	rt, err := New(Config{NewRunner: func(context.Context, RunnerParams) (Runner, error) { created++; return r, nil }, Memory: fakeMemory{}})
	if err != nil {
		t.Fatal(err)
	}
	info := session.NewInfo("s1", "agent1", "u1", "web", session.KindTask, "", time.Now().UTC())
	err = rt.UseFilesystem(context.Background(), info, func(sandbox.Filesystem) error { panic("callback boom") })
	if err == nil || strings.Contains(err.Error(), "boom") {
		t.Fatalf("callback panic error = %v; want a generic non-leaking error", err)
	}
	rt.cache.mu.Lock()
	cs := rt.cache.sessions[info.ID]
	if cs == nil || cs.leases != 0 || cs.failedAdmission || cs.stale || cs.r != r || r.closed {
		rt.cache.mu.Unlock()
		t.Fatalf("callback panic disturbed healthy runner: cs=%#v closed=%t", cs, r.closed)
	}
	rt.cache.mu.Unlock()
	// The runner survives a caller panic and is reused warm on the next hold.
	if err := rt.UseFilesystem(context.Background(), info, func(sandbox.Filesystem) error { return nil }); err != nil {
		t.Fatalf("reuse after callback panic: %v", err)
	}
	if created != 1 {
		t.Fatalf("created=%d, want warm reuse after callback panic", created)
	}
}

func TestUseFilesystemProviderPanicQuarantinesRunner(t *testing.T) {
	r := &sandboxRunner{sess: panicFilesystemSession{Session: sandbox.NopSession()}}
	rt, err := New(Config{NewRunner: func(context.Context, RunnerParams) (Runner, error) { return r, nil }, Memory: fakeMemory{}})
	if err != nil {
		t.Fatal(err)
	}
	info := session.NewInfo("s1", "agent1", "u1", "web", session.KindTask, "", time.Now().UTC())
	err = rt.UseFilesystem(context.Background(), info, func(sandbox.Filesystem) error { return nil })
	if err == nil || strings.Contains(err.Error(), "boom") {
		t.Fatalf("provider panic error = %v; want a generic non-leaking error", err)
	}
	rt.cache.mu.Lock()
	cs := rt.cache.sessions[info.ID]
	if cs == nil || cs.leases != 0 || !cs.failedAdmission || !cs.stale || r.closed {
		rt.cache.mu.Unlock()
		t.Fatalf("provider panic did not quarantine leased runner: cs=%#v closed=%t", cs, r.closed)
	}
	rt.cache.mu.Unlock()
}

type errFilesystemSession struct{ sandbox.Session }

func (errFilesystemSession) WorkingDir() string { return "/workspace" }
func (errFilesystemSession) Filesystem() (sandbox.Filesystem, error) {
	return nil, errors.New("filesystem acquisition failed")
}

type nilFilesystemSession struct{ sandbox.Session }

func (nilFilesystemSession) WorkingDir() string                      { return "/workspace" }
func (nilFilesystemSession) Filesystem() (sandbox.Filesystem, error) { return nil, nil }

// leasesFor reads the counted lease for a session under the cache lock.
func leasesFor(rt *Runtime, id string) int {
	rt.cache.mu.Lock()
	defer rt.cache.mu.Unlock()
	if cs := rt.cache.sessions[id]; cs != nil {
		return cs.leases
	}
	return -1
}

func newFilesystemRuntime(t *testing.T, r Runner) (*Runtime, session.Info) {
	t.Helper()
	rt, err := New(Config{NewRunner: func(context.Context, RunnerParams) (Runner, error) { return r, nil }, Memory: fakeMemory{}})
	if err != nil {
		t.Fatal(err)
	}
	return rt, session.NewInfo("s1", "agent1", "u1", "web", session.KindTask, "", time.Now().UTC())
}

func TestUseFilesystemCallbackErrorReleasesLeaseAndKeepsRunner(t *testing.T) {
	r := &sandboxRunner{sess: filesystemSession{Session: sandbox.NopSession(), root: t.TempDir()}}
	rt, info := newFilesystemRuntime(t, r)
	want := errors.New("callback failed")
	if err := rt.UseFilesystem(context.Background(), info, func(sandbox.Filesystem) error { return want }); !errors.Is(err, want) {
		t.Fatalf("UseFilesystem err = %v, want callback error surfaced", err)
	}
	if got := leasesFor(rt, info.ID); got != 0 {
		t.Fatalf("callback error left leases=%d, want 0", got)
	}
	rt.cache.mu.Lock()
	cs := rt.cache.sessions[info.ID]
	healthy := cs != nil && !cs.stale && !cs.failedAdmission && cs.r == r && !r.closed
	rt.cache.mu.Unlock()
	if !healthy {
		t.Fatalf("callback error disturbed a healthy runner: cs=%#v closed=%t", cs, r.closed)
	}
}

func TestUseFilesystemCanceledContextReleasesLeaseWithoutCallback(t *testing.T) {
	r := &sandboxRunner{sess: filesystemSession{Session: sandbox.NopSession(), root: t.TempDir()}}
	rt, info := newFilesystemRuntime(t, r)
	// Warm the runner so acquisition itself never observes the cancellation.
	if err := rt.UseFilesystem(context.Background(), info, func(sandbox.Filesystem) error { return nil }); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	invoked := false
	err := rt.UseFilesystem(ctx, info, func(sandbox.Filesystem) error { invoked = true; return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("UseFilesystem err = %v, want context.Canceled", err)
	}
	if invoked {
		t.Fatal("callback ran under a canceled context")
	}
	if got := leasesFor(rt, info.ID); got != 0 {
		t.Fatalf("canceled context left leases=%d, want 0", got)
	}
}

func TestUseFilesystemMissingFilesystemCapabilityReleasesLease(t *testing.T) {
	r := &sandboxRunner{sess: sandbox.NopSession()} // a Session that is not a FilesystemSession
	rt, info := newFilesystemRuntime(t, r)
	err := rt.UseFilesystem(context.Background(), info, func(sandbox.Filesystem) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "filesystem capability") {
		t.Fatalf("UseFilesystem err = %v, want a capability error", err)
	}
	if got := leasesFor(rt, info.ID); got != 0 {
		t.Fatalf("capability miss left leases=%d, want 0", got)
	}
	rt.cache.mu.Lock()
	cs := rt.cache.sessions[info.ID]
	quarantined := cs != nil && (cs.stale || cs.failedAdmission)
	rt.cache.mu.Unlock()
	if quarantined {
		t.Fatal("a capability miss must not quarantine an otherwise healthy runner")
	}
}

func TestUseFilesystemAcquisitionErrorReleasesLease(t *testing.T) {
	r := &sandboxRunner{sess: errFilesystemSession{Session: sandbox.NopSession()}}
	rt, info := newFilesystemRuntime(t, r)
	err := rt.UseFilesystem(context.Background(), info, func(sandbox.Filesystem) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "acquisition failed") {
		t.Fatalf("UseFilesystem err = %v, want the acquisition error", err)
	}
	if got := leasesFor(rt, info.ID); got != 0 {
		t.Fatalf("acquisition error left leases=%d, want 0", got)
	}
	if r.closed {
		t.Fatal("acquisition error closed a healthy runner")
	}
}

func TestUseFilesystemRejectsNilFilesystem(t *testing.T) {
	r := &sandboxRunner{sess: nilFilesystemSession{Session: sandbox.NopSession()}}
	rt, info := newFilesystemRuntime(t, r)
	err := rt.UseFilesystem(context.Background(), info, func(f sandbox.Filesystem) error {
		t.Fatal("callback must not run with a nil filesystem")
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "nil filesystem") {
		t.Fatalf("UseFilesystem err = %v, want a nil-filesystem rejection", err)
	}
	if got := leasesFor(rt, info.ID); got != 0 {
		t.Fatalf("nil filesystem left leases=%d, want 0", got)
	}
}

func TestUseFilesystemInvalidationDefersWhileLeased(t *testing.T) {
	r := &sandboxRunner{sess: filesystemSession{Session: sandbox.NopSession(), root: t.TempDir()}}
	rt, info := newFilesystemRuntime(t, r)
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- rt.UseFilesystem(context.Background(), info, func(sandbox.Filesystem) error { close(entered); <-release; return nil })
	}()
	<-entered
	if err := rt.InvalidateSkillPolicy(); err != nil {
		t.Fatal(err)
	}
	if r.closed {
		t.Fatal("skill-policy invalidation closed a leased runner")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := leasesFor(rt, info.ID); got != 0 {
		t.Fatalf("invalidation deferral left leases=%d, want 0", got)
	}
}

func TestUseFilesystemTerminalCloseForceInterruptsLease(t *testing.T) {
	r := &sandboxRunner{sess: filesystemSession{Session: sandbox.NopSession(), root: t.TempDir()}}
	rt, info := newFilesystemRuntime(t, r)
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- rt.UseFilesystem(context.Background(), info, func(sandbox.Filesystem) error { close(entered); <-release; return nil })
	}()
	<-entered
	// Terminal owner delete is the only path allowed to close a leased runner.
	if err := rt.CloseSession(context.Background(), info.ID); err != nil {
		t.Fatal(err)
	}
	if !r.closed {
		t.Fatal("terminal close did not force-interrupt the leased runner")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("callback after terminal close: %v", err)
	}
	// The terminal delete removed the record; the deferred release is a safe no-op.
	if got := leasesFor(rt, info.ID); got != -1 {
		t.Fatalf("terminal close left a cache record with leases=%d", got)
	}
}

func TestUseFilesystemTerminalCloseDuringConstructionRetiresDetachedRunner(t *testing.T) {
	started, releaseFactory := make(chan struct{}), make(chan struct{})
	r := &sandboxRunner{sess: filesystemSession{Session: sandbox.NopSession(), root: t.TempDir()}}
	rt, err := New(Config{NewRunner: func(context.Context, RunnerParams) (Runner, error) {
		close(started)
		<-releaseFactory
		return r, nil
	}, Memory: fakeMemory{}})
	if err != nil {
		t.Fatal(err)
	}
	info := session.NewInfo("close-during-construction", "agent1", "u1", "web", session.KindTask, "", time.Now().UTC())

	callbackCalled := false
	done := make(chan error, 1)
	go func() {
		done <- rt.UseFilesystem(context.Background(), info, func(sandbox.Filesystem) error {
			callbackCalled = true
			return nil
		})
	}()
	<-started
	if err := rt.CloseSession(context.Background(), info.ID); err != nil {
		t.Fatalf("terminal close during construction: %v", err)
	}
	if got := leasesFor(rt, info.ID); got != -1 {
		t.Fatalf("terminal close retained constructing record with leases=%d", got)
	}

	close(releaseFactory)
	if err := <-done; err == nil || !strings.Contains(err.Error(), "runner admission closed") {
		t.Fatalf("UseFilesystem err = %v, want closed admission", err)
	}
	if callbackCalled {
		t.Fatal("filesystem callback ran after terminal close")
	}
	if !r.closed {
		t.Fatal("successful detached construction was not retired")
	}
	if got := leasesFor(rt, info.ID); got != -1 {
		t.Fatalf("detached construction resurrected cache record with leases=%d", got)
	}
}

func TestChatTurnAndFilesystemCallbackHoldIndependentRunnerLeases(t *testing.T) {
	old := &blockingSandboxRunner{
		sandboxRunner: &sandboxRunner{sess: filesystemSession{Session: sandbox.NopSession(), root: t.TempDir()}},
		chatStarted:   make(chan struct{}),
		chatStream:    make(chan Event),
	}
	next := &sandboxRunner{sess: filesystemSession{Session: sandbox.NopSession(), root: t.TempDir()}}
	created := 0
	rt, err := New(Config{NewRunner: func(context.Context, RunnerParams) (Runner, error) {
		created++
		if created == 1 {
			return old, nil
		}
		return next, nil
	}, Memory: fakeMemory{}})
	if err != nil {
		t.Fatal(err)
	}
	info := session.NewInfo("chat-filesystem-leases", "agent1", "u1", "web", session.KindChat, "", time.Now().UTC())

	stream, err := rt.ChatAdmitted(context.Background(), info, "turn")
	if err != nil {
		t.Fatalf("admit chat: %v", err)
	}
	<-old.chatStarted
	rt.cache.mu.Lock()
	cs := rt.cache.sessions[info.ID]
	if cs == nil || cs.r != old || cs.leases != 1 {
		rt.cache.mu.Unlock()
		t.Fatalf("active chat cache=%#v; want old runner with one lease", cs)
	}
	rt.cache.mu.Unlock()

	filesystemEntered, releaseFilesystem := make(chan struct{}), make(chan struct{})
	filesystemDone := make(chan error, 1)
	go func() {
		filesystemDone <- rt.UseFilesystem(context.Background(), info, func(sandbox.Filesystem) error {
			close(filesystemEntered)
			<-releaseFilesystem
			return nil
		})
	}()
	<-filesystemEntered
	rt.cache.mu.Lock()
	if cs.r != old || cs.leases != 2 || created != 1 {
		rt.cache.mu.Unlock()
		t.Fatalf("filesystem selection cache=%#v created=%d; want old runner with two leases", cs, created)
	}
	rt.cache.mu.Unlock()

	if err := rt.ResetRunners(); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if err := rt.InvalidateSkillPolicy(); err != nil {
		t.Fatalf("invalidate policy: %v", err)
	}
	rt.cache.mu.Lock()
	if cs.r != old || cs.leases != 2 || !cs.stale || old.closed {
		rt.cache.mu.Unlock()
		t.Fatalf("reset/invalidation disturbed live leases: cache=%#v closed=%t", cs, old.closed)
	}
	rt.cache.mu.Unlock()

	close(releaseFilesystem)
	if err := <-filesystemDone; err != nil {
		t.Fatalf("filesystem callback: %v", err)
	}
	rt.cache.mu.Lock()
	if cs.r != old || cs.leases != 1 || old.closed {
		rt.cache.mu.Unlock()
		t.Fatalf("filesystem release disturbed turn lease: cache=%#v closed=%t", cs, old.closed)
	}
	rt.cache.mu.Unlock()

	close(old.chatStream)
	for event := range stream {
		if event.Err != nil {
			t.Fatalf("chat event: %v", event.Err)
		}
	}
	if err := rt.WaitTurns(context.Background()); err != nil {
		t.Fatalf("wait turns: %v", err)
	}
	rt.cache.mu.Lock()
	if cs.r != old || cs.leases != 0 || old.closed {
		rt.cache.mu.Unlock()
		t.Fatalf("completed turn cache=%#v closed=%t; want stale runner retained until lookup", cs, old.closed)
	}
	rt.cache.mu.Unlock()

	if err := rt.UseFilesystem(context.Background(), info, func(sandbox.Filesystem) error { return nil }); err != nil {
		t.Fatalf("later lookup: %v", err)
	}
	rt.cache.mu.Lock()
	if cs.r != next || created != 2 || !old.closed || cs.leases != 0 {
		rt.cache.mu.Unlock()
		t.Fatalf("later lookup cache=%#v created=%d oldClosed=%t; want replacement", cs, created, old.closed)
	}
	rt.cache.mu.Unlock()
}
