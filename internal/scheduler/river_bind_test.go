package scheduler

// Issue #708 Section B: the shared River client is a one-shot pre-start bind.
// BindRiverClient rejects a nil client (missing), a second bind (duplicate), and
// a bind after the subsystem has started (late).

import (
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

func newBindTestService() *Service { return &Service{} }
