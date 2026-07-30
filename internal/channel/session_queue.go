package channel

import (
	"context"
	"sync"
	"time"

	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

// sessionQueue enforces a per-session FIFO execution order for incoming
// channel messages. Only one active request per session runs at a time;
// later requests queue up and are dispatched in arrival order after the
// current one finishes or is aborted.
//
// The queue is keyed by the resolved Stella session key so the boundary
// matches the actual memory/history boundary rather than a platform-specific
// display concept.
type sessionQueue struct {
	mu          sync.Mutex
	sessions    map[string]*sessionSlot
	idleTimeout time.Duration
}

// sessionSlot holds per-session execution state.
type sessionSlot struct {
	parent       *sessionQueue
	key          string
	mu           sync.Mutex
	queue        chan queuedRequest
	activeCancel context.CancelFunc // cancel func for the currently-running request
	refs         int
}

// queuedRequest carries the work unit that will be dispatched.
type queuedRequest struct {
	ctx     context.Context
	fn      func(context.Context) (*pkgchannel.ChatStream, error)
	resultC chan queueResult
}

type queueResult struct {
	stream *pkgchannel.ChatStream
	doneC  chan struct{} // caller signals via close(doneC) when stream is fully consumed
	err    error
}

// newSessionQueue creates an empty queue.
func newSessionQueue() *sessionQueue {
	return newSessionQueueWithIdleTimeout(5 * time.Minute)
}

func newSessionQueueWithIdleTimeout(idleTimeout time.Duration) *sessionQueue {
	return &sessionQueue{
		sessions:    make(map[string]*sessionSlot),
		idleTimeout: idleTimeout,
	}
}

// getOrCreate returns the slot for sessionKey, creating it on first access.
func (q *sessionQueue) getOrCreate(sessionKey string) *sessionSlot {
	q.mu.Lock()
	defer q.mu.Unlock()
	if s, ok := q.sessions[sessionKey]; ok {
		s.refs++
		return s
	}
	s := &sessionSlot{
		parent: q,
		key:    sessionKey,
		queue:  make(chan queuedRequest, 64),
		refs:   1,
	}
	q.sessions[sessionKey] = s
	go s.run()
	return s
}

func (q *sessionQueue) release(slot *sessionSlot) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if current, ok := q.sessions[slot.key]; !ok || current != slot || slot.refs == 0 {
		return
	}
	slot.refs--
}

func (s *sessionSlot) clearActiveCancel() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.activeCancel = nil
}

func (q *sessionQueue) tryDeleteIdle(slot *sessionSlot) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	current, ok := q.sessions[slot.key]
	if !ok || current != slot || len(slot.queue) != 0 || slot.refs != 0 {
		return false
	}

	slot.mu.Lock()
	active := slot.activeCancel != nil
	slot.mu.Unlock()
	if active {
		return false
	}

	delete(q.sessions, slot.key)
	return true
}

// Enqueue adds work to the session's queue and waits for the chat function to
// return a *ChatStream. It returns the stream and a doneC channel.
//
// The caller MUST close doneC after it has fully consumed (or abandoned) the
// stream. Closing doneC signals the queue that the slot is free and the next
// queued message can be dispatched.
//
// If ctx is cancelled before the work starts (or while waiting), Enqueue returns
// the context error. The caller does not receive a doneC in that case.
func (q *sessionQueue) Enqueue(
	ctx context.Context,
	sessionKey string,
	fn func(context.Context) (*pkgchannel.ChatStream, error),
) (*pkgchannel.ChatStream, chan struct{}, error) {
	slot := q.getOrCreate(sessionKey)
	resultC := make(chan queueResult, 1)
	req := queuedRequest{
		ctx:     ctx,
		fn:      fn,
		resultC: resultC,
	}
	select {
	case slot.queue <- req:
		q.release(slot)
	case <-ctx.Done():
		q.release(slot)
		return nil, nil, ctx.Err()
	}
	select {
	case res := <-resultC:
		return res.stream, res.doneC, res.err
	case <-ctx.Done():
		// Wait for the queue worker in the background to send the result.
		// If it produced a stream, we must clean it up to prevent a slot leak.
		// doneC is closed whether or not a stream came back: the worker blocks on
		// it, so a stream-less operation would otherwise wedge the session.
		go func() {
			res := <-resultC
			if res.stream != nil {
				go func() {
					for range res.stream.Events {
					}
				}()
			}
			if res.doneC != nil {
				close(res.doneC)
			}
		}()
		return nil, nil, ctx.Err()
	}
}

// EnqueueControl runs a non-streaming session operation (e.g. /new) in the same
// per-session FIFO order as chat turns and waits for it to finish. Rotating a
// session ahead of an in-flight turn would strand that turn's reply in a session
// the user has already left, so control operations wait their turn instead.
func (q *sessionQueue) EnqueueControl(ctx context.Context, sessionKey string, fn func(context.Context) error) error {
	var opErr error
	_, doneC, err := q.Enqueue(ctx, sessionKey, func(qctx context.Context) (*pkgchannel.ChatStream, error) {
		opErr = fn(qctx)
		return nil, nil
	})
	if err != nil {
		return err
	}
	close(doneC)
	return opErr
}

// Abort cancels the currently-running request for sessionKey.
// Returns true if a request was active and was cancelled.
func (q *sessionQueue) Abort(sessionKey string) bool {
	q.mu.Lock()
	slot, ok := q.sessions[sessionKey]
	q.mu.Unlock()
	if !ok {
		return false
	}
	slot.mu.Lock()
	cancel := slot.activeCancel
	slot.mu.Unlock()
	if cancel == nil {
		return false
	}
	cancel()
	return true
}

// run is the per-session goroutine that dispatches queued requests one at a time.
func (s *sessionSlot) run() {
	for {
		timer := time.NewTimer(s.parent.idleTimeout)

		select {
		case req := <-s.queue:
			if !timer.Stop() {
				<-timer.C
			}

			// Skip requests whose caller already gave up.
			if req.ctx.Err() != nil {
				req.resultC <- queueResult{err: req.ctx.Err()}
				continue
			}

			// Create a cancellable child context so /abort can stop this request.
			ctx, cancel := context.WithCancel(req.ctx)

			s.mu.Lock()
			s.activeCancel = cancel
			s.mu.Unlock()

			stream, err := req.fn(ctx)
			if err != nil {
				cancel()
				s.clearActiveCancel()
				req.resultC <- queueResult{err: err}
				continue
			}

			// doneC lets the caller signal when the stream has been fully consumed.
			// The queue worker waits on it before dispatching the next request,
			// ensuring history is written in order.
			doneC := make(chan struct{})
			req.resultC <- queueResult{stream: stream, doneC: doneC}

			<-doneC
			cancel()
			s.clearActiveCancel()
		case <-timer.C:
			if s.parent.tryDeleteIdle(s) {
				return
			}
		}
	}
}
