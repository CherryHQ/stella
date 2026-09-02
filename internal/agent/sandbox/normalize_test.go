package sandbox

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

func TestFormatToolDuration(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{500 * time.Microsecond, "500µs"},
		{5 * time.Millisecond, "5ms"},
		{999 * time.Millisecond, "999ms"},
		{1500 * time.Millisecond, "1.5s"},
		{59 * time.Second, "59.0s"},
		{2 * time.Minute, "120s"},
	}
	for _, tc := range tests {
		got := formatToolDuration(tc.d)
		if got != tc.want {
			t.Errorf("formatToolDuration(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

func TestNormalizeExec_stdoutOnly(t *testing.T) {
	r := normalizeExec(pkgsandbox.ExecResult{Stdout: "hello", ExitCode: 0}, 0)
	if !strings.HasPrefix(r.Content, "hello") {
		t.Errorf("unexpected content: %q", r.Content)
	}
	if r.IsError {
		t.Error("expected IsError=false for exit code 0")
	}
}

func TestNormalizeExec_stderrAppended(t *testing.T) {
	r := normalizeExec(pkgsandbox.ExecResult{Stdout: "out", Stderr: "err", ExitCode: 1}, 0)
	if !strings.Contains(r.Content, "out") || !strings.Contains(r.Content, "err") {
		t.Errorf("expected both stdout and stderr in content, got: %q", r.Content)
	}
	if !r.IsError {
		t.Error("expected IsError=true for non-zero exit code")
	}
}

func TestNormalizeExec_includesTimestamp(t *testing.T) {
	r := normalizeExec(pkgsandbox.ExecResult{Stdout: "hi", ExitCode: 0}, time.Second)
	if !strings.Contains(r.Content, "[exit:0") {
		t.Errorf("expected exit code in content, got: %q", r.Content)
	}
}

func TestNormalizeError_generic(t *testing.T) {
	r := normalizeToolError(errors.New("boom"), "bash")
	if !strings.Contains(r.Content, "bash") || !strings.Contains(r.Content, "boom") {
		t.Errorf("unexpected content: %q", r.Content)
	}
	if !r.IsError {
		t.Error("expected IsError=true")
	}
}

func TestNormalizeError_timeout(t *testing.T) {
	r := normalizeToolError(context.DeadlineExceeded, "bash")
	if !strings.Contains(r.Content, "timed out") {
		t.Errorf("expected timeout message, got: %q", r.Content)
	}
}

func TestNormalizeError_canceled(t *testing.T) {
	r := normalizeToolError(context.Canceled, "bash")
	if !strings.Contains(r.Content, "aborted") {
		t.Errorf("expected aborted message, got: %q", r.Content)
	}
}
