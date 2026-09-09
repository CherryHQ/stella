package local

import (
	"context"

	sandboxpkg "github.com/CherryHQ/stella/pkg/sandbox"
)

// ObserveResource reports only proof available from this raw session. The
// platform-specific reaping path is responsible for clearing everNativeStarted;
// Close returning nil is not itself an absence proof.
func (s *localSession) ObserveResource(ctx context.Context) (sandboxpkg.ResourceObservation, error) {
	if err := ctx.Err(); err != nil {
		return sandboxpkg.ResourceObservation{}, err
	}
	s.mu.RLock()
	closed, owners, nativeStarted := s.closed, len(s.procs), s.everNativeStarted
	s.mu.RUnlock()
	if !closed {
		return sandboxpkg.ResourceObservation{State: sandboxpkg.ResourceStatePresent, Detail: "local session is still open"}, nil
	}
	if owners != 0 {
		return sandboxpkg.ResourceObservation{State: sandboxpkg.ResourceStateUnknown, Detail: "local session still has tracked process owners"}, nil
	}
	if nativeStarted {
		return sandboxpkg.ResourceObservation{State: sandboxpkg.ResourceStateUnknown, Detail: "local session cannot prove native descendants are gone"}, nil
	}
	return sandboxpkg.ResourceObservation{State: sandboxpkg.ResourceStateAbsent, Detail: "local session closed after all process owners were reaped"}, nil
}

var _ sandboxpkg.ResourceObservationProvider = (*localSession)(nil)
