// Internal tests for unexported helpers in traced.go.
package memory

import (
	"testing"
)

func TestTruncateStr(t *testing.T) {
	tests := []struct {
		s         string
		maxRunes  int
		truncated bool
	}{
		{"hello", 10, false},
		{"hello world", 5, true},
		{"日本語", 2, true},
	}
	for _, tc := range tests {
		got := truncateStr(tc.s, tc.maxRunes)
		if tc.truncated {
			if len(got) == 0 {
				t.Errorf("truncateStr(%q, %d): expected non-empty result", tc.s, tc.maxRunes)
			}
		} else {
			if got != tc.s {
				t.Errorf("truncateStr(%q, %d): expected unchanged, got %q", tc.s, tc.maxRunes, got)
			}
		}
	}
}

func TestErrCapabilityNotSupported(t *testing.T) {
	err := errCapabilityNotSupported("TestCap")
	if err == nil {
		t.Fatal("expected non-nil error")
	}
	if err.Error() == "" {
		t.Error("expected non-empty error message")
	}
}
