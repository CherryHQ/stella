// Package turnqueue serializes synchronous agent-originated turns per Session.
package turnqueue

import (
	"container/list"
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
	mu     sync.Mutex
	queue  list.List
	wake   chan struct{}
	refs   int
}

type requestState uint8

const (
	requestQueued requestState = iota
	requestDequeued
	requestStarted
	requestAbandoned
	requestCompleted
)

type request struct {
	ctx      context.Context
	deadline time.Time
	fn       func(context.Context, context.Context, func() error) error
	result   chan error
	slot     *slot
	element  *list.Element
	state    requestState
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
	s := &slot{parent: q, key: key, wake: make(chan struct{}, 1), refs: 1}
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
	if q.sessions[s.key] != s || s.refs != 0 {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.queue.Len() != 0 {
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
	req := &request{ctx: ctx, deadline: time.Now().Add(q.hold), fn: fn, result: make(chan error, 1), slot: s, state: requestQueued}
	s.mu.Lock()
	if s.queue.Len() >= q.depth {
		s.mu.Unlock()
		q.release(s)
		return ErrFull
	}
	req.element = s.queue.PushBack(req)
	s.signal()
	s.mu.Unlock()
	q.release(s)

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
	r.slot.mu.Lock()
	defer r.slot.mu.Unlock()
	switch r.state {
	case requestStarted, requestCompleted:
		return false
	case requestQueued:
		r.slot.queue.Remove(r.element)
		r.element = nil
	}
	r.state = requestAbandoned
	return true
}

func (r *request) beforeStart() error {
	r.slot.mu.Lock()
	defer r.slot.mu.Unlock()
	if r.state == requestAbandoned {
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
		r.state = requestAbandoned
		return ErrTimeout
	}
	r.state = requestStarted
	return nil
}

func (s *slot) run() {
	for {
		if req := s.dequeue(); req != nil {
			s.execute(req)
			continue
		}
		timer := time.NewTimer(s.parent.idle)
		select {
		case <-s.wake:
			if !timer.Stop() {
				<-timer.C
			}
		case <-timer.C:
			if s.parent.deleteIdle(s) {
				return
			}
		}
	}
}

func (s *slot) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *slot) dequeue() *request {
	s.mu.Lock()
	defer s.mu.Unlock()
	element := s.queue.Front()
	if element == nil {
		return nil
	}
	s.queue.Remove(element)
	req := element.Value.(*request)
	req.element = nil
	req.state = requestDequeued
	return req
}

func (s *slot) execute(req *request) {
	var err error
	if ctxErr := req.ctx.Err(); ctxErr != nil {
		err = ctxErr
	} else {
		waitCtx, cancel := context.WithDeadline(req.ctx, req.deadline)
		err = req.fn(waitCtx, req.ctx, req.beforeStart)
		cancel()
	}
	s.mu.Lock()
	req.state = requestCompleted
	s.mu.Unlock()
	req.result <- err
}
