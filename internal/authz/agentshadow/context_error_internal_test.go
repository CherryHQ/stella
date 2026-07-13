package agentshadow

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
)

// isContextError must recognize a cancellation/deadline error however deeply it
// is wrapped (pgx wraps, policy.revision wraps with %w, Begin wraps again with
// %w), and must NOT swallow a genuine error.
func TestIsContextError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"canceled", context.Canceled, true},
		{"deadline", context.DeadlineExceeded, true},
		{"wrapped-canceled", fmt.Errorf("authz/policy: read revision: %w", context.Canceled), true},
		{
			"double-wrapped-deadline",
			fmt.Errorf("%w: %w", authz.ErrAuthorizerUnavailable,
				fmt.Errorf("authz/policy: read revision: %w", context.DeadlineExceeded)),
			true,
		},
		{"genuine-error", errors.New("connection refused"), false},
		{"unavailable-without-context", authz.ErrAuthorizerUnavailable, false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		if got := isContextError(tc.err); got != tc.want {
			t.Errorf("%s: isContextError = %v, want %v", tc.name, got, tc.want)
		}
	}
}
