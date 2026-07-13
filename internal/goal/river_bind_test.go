package goal

// Issue #708 Section B: the shared River client is a one-shot pre-start bind.
// BindRiverClient rejects a nil client (missing), a second bind (duplicate), and
// a bind after the subsystem has started (late).

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
)

func TestBindRiverClientGuards(t *testing.T) {
	dummy := &river.Client[pgx.Tx]{}

	if err := newBindTestService().BindRiverClient(nil); err == nil {
		t.Error("BindRiverClient(nil) should error (missing)")
	}

	s := newBindTestService()
	if err := s.BindRiverClient(dummy); err != nil {
		t.Fatalf("first BindRiverClient: %v", err)
	}
	if err := s.BindRiverClient(dummy); err == nil {
		t.Error("second BindRiverClient should error (duplicate)")
	}

	late := newBindTestService()
	late.started = true
	if err := late.BindRiverClient(dummy); err == nil {
		t.Error("BindRiverClient after start should error (late)")
	}
}

// TestBindRiverClientConcurrent asserts the bind is mutually exclusive: when many
// goroutines race to bind the same client, exactly one succeeds and the rest get
// the duplicate error. The outcome (one winner) is deterministic; the mutex is
// what makes it so — run with -race to also catch a data race on river/started.
func TestBindRiverClientConcurrent(t *testing.T) {
	s := newBindTestService()
	dummy := &river.Client[pgx.Tx]{}

	const n = 32
	var wg sync.WaitGroup
	var successes int64
	wg.Add(n)
	for range n {
		go func() {
			defer wg.Done()
			if err := s.BindRiverClient(dummy); err == nil {
				atomic.AddInt64(&successes, 1)
			}
		}()
	}
	wg.Wait()

	if successes != 1 {
		t.Fatalf("expected exactly 1 successful concurrent bind, got %d", successes)
	}
}

func newBindTestService() *Service { return &Service{Dispatcher: &Dispatcher{}} }
