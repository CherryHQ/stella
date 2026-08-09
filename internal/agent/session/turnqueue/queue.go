// Package turnqueue serializes synchronous agent-originated turns per Session.
package turnqueue

import (
	"context"
	"errors"
	"sync"
	"time"
)

const (
	// MaxDepth bounds callers waiting behind a Session turn. Raise it only when
	// production fan-in shows legitimate bursts are being rejected.
	MaxDepth = 32
	// HoldTimeout bounds queue admission, not the admitted turn. Revisit when
	// observed Session turns routinely create legitimate waits beyond this limit.
	HoldTimeout = 30 * time.Second
	idleTimeout = 5 * time.Minute
)

var (
	ErrFull    = errors.New("session turn queue is full")
	ErrTimeout = errors.New("timed out waiting for session turn admission")
)

// Queue provides process-local FIFO fairness within one replica. Cluster-wide
// one-active-turn-per-Session belongs to #643, tracked under #637: its shared
// lease will replace every in-process admission guard for all turn ingress.
// Queue must remain only a fairness layer in front of that standard admission
// seam, never a send-specific correctness boundary competing with the lease.
type Queue struct {
	mu       sync.Mutex
	sessions map[string]*slot
	hold     time.Duration
	idle     time.Duration
	depth    int
}

type slot struct {
	parent *Queue
	key    string
	queue  chan *request
	refs   int
}

type request struct {
	ctx      context.Context
	deadline time.Time
	fn       func(context.Context, context.Context, func() error) error
	result   chan error

	mu        sync.Mutex
	started   bool
	abandoned bool
}

func New() *Queue { return NewWithLimits(MaxDepth, HoldTimeout, idleTimeout) }

func NewWithLimits(depth int, hold, idle time.Duration) *Queue {
	return &Queue{sessions: make(map[string]*slot), depth: depth, hold: hold, idle: idle}
}

func (q *Queue) getOrCreate(key string) *slot {
	q.mu.Lock()
	defer q.mu.Unlock()
	if s := q.sessions[key]; s != nil {
		s.refs++
		return s
	}
	s := &slot{parent: q, key: key, queue: make(chan *request, q.depth), refs: 1}
	q.sessions[key] = s
	go s.run()
	return s
}

func (q *Queue) release(s *slot) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.sessions[s.key] == s && s.refs > 0 {
		s.refs--
	}
}

func (q *Queue) deleteIdle(s *slot) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.sessions[s.key] != s || s.refs != 0 || len(s.queue) != 0 {
		return false
	}
	delete(q.sessions, s.key)
	return true
}

// Enqueue runs fn in per-key FIFO order. beforeStart must be called by fn after
// acquiring the runtime single-flight guard and before appending input or
// starting tools. It closes the cancellation race in the same way as the
// channel queue's EnqueueControl started contract: a timeout/cancellation that
// returns before admission makes every later beforeStart fail.
func (q *Queue) Enqueue(ctx context.Context, key string, fn func(context.Context, context.Context, func() error) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s := q.getOrCreate(key)
	req := &request{ctx: ctx, deadline: time.Now().Add(q.hold), fn: fn, result: make(chan error, 1)}
	select {
	case s.queue <- req:
		q.release(s)
	default:
		q.release(s)
		return ErrFull
	}

	timer := time.NewTimer(time.Until(req.deadline))
	defer timer.Stop()
	timerC := timer.C
	ctxDone := ctx.Done()
	for {
		select {
		case err := <-req.result:
			return err
		case <-ctxDone:
			if req.abandon() {
				return ctx.Err()
			}
			// Admission won the cancellation race. Let the context-respecting turn
			// unwind before returning so caller-owned result state cannot race it.
			ctxDone = nil
		case <-timerC:
			if req.abandon() {
				return ErrTimeout
			}
			// Admission won the boundary race; the hold limit no longer applies.
			timerC = nil
		}
	}
}

func (r *request) abandon() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.started {
		return false
	}
	r.abandoned = true
	return true
}

func (r *request) beforeStart() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.abandoned {
		if err := r.ctx.Err(); err != nil {
			return err
		}
		return ErrTimeout
	}
	// Check the caller's exact context, not a derived child: parent cancellation
	// can be observed before it propagates to children (sessionQueue precedent).
	if err := r.ctx.Err(); err != nil {
		return err
	}
	if !time.Now().Before(r.deadline) {
		r.abandoned = true
		return ErrTimeout
	}
	r.started = true
	return nil
}

func (s *slot) run() {
	for {
		timer := time.NewTimer(s.parent.idle)
		select {
		case req := <-s.queue:
			if !timer.Stop() {
				<-timer.C
			}
			if err := req.ctx.Err(); err != nil {
				req.result <- err
				continue
			}
			waitCtx, cancel := context.WithDeadline(req.ctx, req.deadline)
			req.result <- req.fn(waitCtx, req.ctx, req.beforeStart)
			cancel()
		case <-timer.C:
			if s.parent.deleteIdle(s) {
				return
			}
		}
	}
}
