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

// A timeout is the harness killing the command, not the command answering, so
// it must not arrive as a command exit.
func TestBashTimeoutIsNotACommandExit(t *testing.T) {
	session := &bashSecretSession{Session: pkgsandbox.NopSession(), result: pkgsandbox.ExecResult{ExitCode: -1}}
	tool := newBashTool(session, "", NewSessionSecretValues())

	_, err := tool.Execute(context.Background(), map[string]any{"command": "sleep 99", "timeout": 1})

	var exitErr *ai.CommandExitError
	if err == nil || errors.As(err, &exitErr) {
		t.Fatalf("err = %v, want a plain timeout error", err)
	}
}
