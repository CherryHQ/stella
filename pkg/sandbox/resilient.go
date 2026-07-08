package sandbox

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
)

// SessionCreator creates a new session. Used by ResilientSession to recreate
// the underlying session when it is found closed.
type SessionCreator func(ctx context.Context) (Session, error)

// ResilientSession wraps a Session and transparently recreates it when the
// underlying session has been closed unexpectedly (e.g. container exited or
// sandbox process crashed). Explicit Close calls are permanent — no recreation
// after that.
type ResilientSession struct {
	create SessionCreator
	mu     sync.Mutex
	inner  Session
	closed bool
	log    *slog.Logger
}

// NewResilientSession wraps an existing session with auto-recreation support.
func NewResilientSession(session Session, create SessionCreator) *ResilientSession {
	return &ResilientSession{
		create: create,
		inner:  session,
		log:    slog.With("component", "resilient_session"),
	}
}

// ensureAlive returns the inner session if alive, or recreates it.
// Caller must NOT hold r.mu.
func (r *ResilientSession) ensureAlive(ctx context.Context) (Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.closed {
		return nil, fmt.Errorf("sandbox: session is permanently closed")
	}

	if r.inner != nil && r.inner.Alive() {
		return r.inner, nil
	}

	r.log.Info("recreating sandbox session")
	session, err := r.create(ctx)
	if err != nil {
		return nil, fmt.Errorf("sandbox: recreate session: %w", err)
	}

	r.inner = session
	return session, nil
}

func (r *ResilientSession) Policy() Policy {
	r.mu.Lock()
	s := r.inner
	r.mu.Unlock()
	if s == nil {
		return Policy{}
	}
	return s.Policy()
}

func (r *ResilientSession) WorkingDir() string {
	r.mu.Lock()
	s := r.inner
	r.mu.Unlock()
	if s == nil {
		return ""
	}
	return s.WorkingDir()
}

func (r *ResilientSession) Alive() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return false
	}
	return r.inner != nil && r.inner.Alive()
}

func (r *ResilientSession) Done() <-chan struct{} {
	r.mu.Lock()
	s := r.inner
	r.mu.Unlock()
	if s == nil {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	return s.Done()
}

func (r *ResilientSession) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	if r.inner != nil {
		return r.inner.Close()
	}
	return nil
}

func (r *ResilientSession) Exec(ctx context.Context, command string, opts ExecOptions) (ExecResult, error) {
	s, err := r.ensureAlive(ctx)
	if err != nil {
		return ExecResult{}, err
	}
	return s.Exec(ctx, command, opts)
}

func (r *ResilientSession) StartProcess(ctx context.Context, req ProcessRequest) (ProcessHandle, error) {
	s, err := r.ensureAlive(ctx)
	if err != nil {
		return nil, err
	}
	return s.StartProcess(ctx, req)
}

func (r *ResilientSession) ResolvePath(path string) (string, error) {
	r.mu.Lock()
	s := r.inner
	r.mu.Unlock()
	if s == nil {
		return "", fmt.Errorf("sandbox: no active session")
	}
	return s.ResolvePath(path)
}

func (r *ResilientSession) ResolveWritePath(path string) (string, error) {
	r.mu.Lock()
	s := r.inner
	r.mu.Unlock()
	if s == nil {
		return "", fmt.Errorf("sandbox: no active session")
	}
	return s.ResolveWritePath(path)
}
