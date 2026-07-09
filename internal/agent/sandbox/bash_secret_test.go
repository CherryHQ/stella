package sandbox

import (
	"context"
	"errors"
	"strings"
	"testing"

	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

type bashSecretSession struct {
	pkgsandbox.Session
	result  pkgsandbox.ExecResult
	execErr error
}

func (s *bashSecretSession) Exec(context.Context, string, pkgsandbox.ExecOptions) (pkgsandbox.ExecResult, error) {
	if s.execErr != nil {
		return s.result, s.execErr
	}
	return s.result, nil
}

func TestBashSessionSecretValueRedactedFromStdout(t *testing.T) {
	secrets := NewSessionSecretValues()
	secrets.Set([]string{"vault-secret-value"})
	session := &bashSecretSession{Session: pkgsandbox.NopSession(), result: pkgsandbox.ExecResult{Stdout: "vault-secret-value\n", ExitCode: 0}}
	tool := newBashTool(session, "", "", secrets)

	content, err := tool.Execute(context.Background(), map[string]any{"command": "printf $MY_SECRET"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	assertSecretRedacted(t, content, "vault-secret-value")
}

func TestBashSessionSecretValueRedactedFromExitCodeResult(t *testing.T) {
	secrets := NewSessionSecretValues()
	secrets.Set([]string{"vault-secret-value"})
	session := &bashSecretSession{Session: pkgsandbox.NopSession(), result: pkgsandbox.ExecResult{Stderr: "bad vault-secret-value\n", ExitCode: 1}}
	tool := newBashTool(session, "", "", secrets)

	content, err := tool.Execute(context.Background(), map[string]any{"command": "false"})
	if err == nil {
		t.Fatal("Execute succeeded, want error")
	}
	assertSecretRedacted(t, content, "vault-secret-value")
}

func TestBashSessionSecretValueRedactedFromExecError(t *testing.T) {
	secrets := NewSessionSecretValues()
	secrets.Set([]string{"vault-secret-value"})
	session := &bashSecretSession{Session: pkgsandbox.NopSession(), execErr: errors.New("runner failed with vault-secret-value")}
	tool := newBashTool(session, "", "", secrets)

	content, err := tool.Execute(context.Background(), map[string]any{"command": "printf $MY_SECRET"})
	if err == nil {
		t.Fatal("Execute succeeded, want error")
	}
	assertSecretRedacted(t, content, "vault-secret-value")
}

func TestRedactSecretValuesReplacesLongestSecretsFirst(t *testing.T) {
	content := "token abc123 also abc"
	for range 100 {
		redacted := redactSecretValues(content, []string{"abc", "abc123"})
		if strings.Contains(redacted, "123") || strings.Contains(redacted, "abc") {
			t.Fatalf("redacted = %q, want no partial secret suffix", redacted)
		}
	}
}

func TestBashNilAndEmptySessionSecretValuesSafe(t *testing.T) {
	session := &bashSecretSession{Session: pkgsandbox.NopSession(), result: pkgsandbox.ExecResult{Stdout: "plain\n", ExitCode: 0}}
	tool := newBashTool(session, "", "", nil)

	content, err := tool.Execute(context.Background(), map[string]any{"command": "printf plain"})
	if err != nil {
		t.Fatalf("Execute with nil secrets: %v", err)
	}
	if strings.Contains(content, "[REDACTED_SECRET]") {
		t.Fatalf("content = %q, want no redaction", content)
	}

	if got := redactSecretValues("plain", NewSessionSecretValues().Values()); got != "plain" {
		t.Fatalf("empty secret redaction = %q, want plain", got)
	}
}

func assertSecretRedacted(t *testing.T, content string, secret string) {
	t.Helper()
	if strings.Contains(content, secret) || !strings.Contains(content, "[REDACTED_SECRET]") {
		t.Fatalf("content = %q, want redacted secret", content)
	}
}
