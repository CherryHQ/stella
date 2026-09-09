package none

import (
	"context"

	sandboxpkg "github.com/CherryHQ/stella/pkg/sandbox"
)

// ObserveResource reports only proof available from this raw session. The
// none backend cannot account for detached descendants after any native
// process has started, even when Close returns nil.
func (s *noneSession) ObserveResource(ctx context.Context) (sandboxpkg.ResourceObservation, error) {
	if err := ctx.Err(); err != nil {
		return sandboxpkg.ResourceObservation{}, err
	}
	s.mu.RLock()
	closed, owners, nativeStarted := s.closed, len(s.procs), s.everNativeStarted
	s.mu.RUnlock()
	if !closed {
		return sandboxpkg.ResourceObservation{State: sandboxpkg.ResourceStatePresent, Detail: "none session is still open"}, nil
	}
	if owners != 0 {
		return sandboxpkg.ResourceObservation{State: sandboxpkg.ResourceStateUnknown, Detail: "none session still has tracked process owners"}, nil
	}
	if nativeStarted {
		return sandboxpkg.ResourceObservation{State: sandboxpkg.ResourceStateUnknown, Detail: "none session cannot prove native descendants are gone"}, nil
	}
	return sandboxpkg.ResourceObservation{State: sandboxpkg.ResourceStateAbsent, Detail: "none session closed without starting a native process"}, nil
}

var _ sandboxpkg.ResourceObservationProvider = (*noneSession)(nil)
