package channel

import (
	"context"
	"sync"

	pkgchannel "github.com/vaayne/anna/pkg/channel"
)

// sessionQueue enforces a per-session FIFO execution order for incoming
// channel messages. Only one active request per session runs at a time;
// later requests queue up and are dispatched in arrival order after the
// current one finishes or is aborted.
//
// The queue is keyed by the resolved Anna session key so the boundary
// matches the actual memory/history boundary rather than a platform-specific
// display concept.
type sessionQueue struct {
	mu       sync.Mutex
	sessions map[string]*sessionSlot
}

// sessionSlot holds per-session execution state.
type sessionSlot struct {
	mu           sync.Mutex
	queue        chan queuedRequest
	activeCancel context.CancelFunc // cancel func for the currently-running request
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
	return &sessionQueue{
		sessions: make(map[string]*sessionSlot),
	}
}

// getOrCreate returns the slot for sessionKey, creating it on first access.
func (q *sessionQueue) getOrCreate(sessionKey string) *sessionSlot {
	q.mu.Lock()
	defer q.mu.Unlock()
	if s, ok := q.sessions[sessionKey]; ok {
		return s
	}
	s := &sessionSlot{
		queue: make(chan queuedRequest, 64),
	}
	q.sessions[sessionKey] = s
	go s.run()
	return s
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
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
	select {
	case res := <-resultC:
		return res.stream, res.doneC, res.err
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
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
	for req := range s.queue {
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

		// Clear active cancel now that execution has begun (stream is async).
		s.mu.Lock()
		s.activeCancel = nil
		s.mu.Unlock()

		if err != nil {
			cancel()
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
	}
}
