package sandbox

import (
	"context"
	"errors"
	"testing"

	"github.com/CherryHQ/stella/pkg/ai"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

// The exit code has to leave the tool as a type. Every consumer that needed
// "did the command fail or did the tool fail" was reading it back out of the
// result text, and text is not a contract.
func TestBashReportsANonzeroExitAsATypedError(t *testing.T) {
	session := &bashSecretSession{Session: pkgsandbox.NopSession(), result: pkgsandbox.ExecResult{Stderr: "no such file\n", ExitCode: 2}}
	tool := newBashTool(session, "", NewSessionSecretValues())

	_, err := tool.Execute(context.Background(), map[string]any{"command": "cat missing"})

	var exitErr *ai.CommandExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("err = %v (%T), want *ai.CommandExitError", err, err)
	}
	if exitErr.ExitCode != 2 || exitErr.Tool != "bash" {
		t.Errorf("exit error = %+v, want bash exit 2", exitErr)
	}
}

// An explicit timeout is a structured command outcome, not a formatted string
// or a command-selected exit status.
func TestBashTimeoutIsATypedCommandTimeout(t *testing.T) {
	session := &bashSecretSession{Session: pkgsandbox.NopSession(), result: pkgsandbox.ExecResult{ExitCode: -1}}
	tool := newBashTool(session, "", NewSessionSecretValues())

	_, err := tool.Execute(context.Background(), map[string]any{"command": "sleep 99", "timeout": 1})

	var exitErr *ai.CommandExitError
	var timeoutErr *ai.CommandTimeoutError
	if err == nil || errors.As(err, &exitErr) || !errors.As(err, &timeoutErr) {
		t.Fatalf("err = %v, want *ai.CommandTimeoutError", err)
	}
}

// The sandbox enforces its own deadline whether or not the call asked for one,
// and it reports that kill the same way: err nil, exit -1. Keying the
// distinction on the caller's timeout argument would file every policy kill as
// a command that ran and answered -1.
func TestBashKillWithoutAnExplicitTimeoutIsNotACommandExit(t *testing.T) {
	session := &bashSecretSession{Session: pkgsandbox.NopSession(), result: pkgsandbox.ExecResult{Stderr: "killed\n", ExitCode: -1}}
	tool := newBashTool(session, "", NewSessionSecretValues())

	_, err := tool.Execute(context.Background(), map[string]any{"command": "sleep 99"})

	var exitErr *ai.CommandExitError
	if err == nil || errors.As(err, &exitErr) {
		t.Fatalf("err = %v (%T), want a plain kill error", err, err)
	}
}
