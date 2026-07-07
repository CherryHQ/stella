package sandbox

import (
	"context"
	"errors"
	"strings"
	"testing"

	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

type secretCaptureSession struct {
	pkgsandbox.Session
	lastEnv map[string]string
	result  pkgsandbox.ExecResult
}

func (s *secretCaptureSession) Exec(_ context.Context, _ string, opts pkgsandbox.ExecOptions) (pkgsandbox.ExecResult, error) {
	s.lastEnv = opts.Env
	if s.result.Stdout != "" || s.result.Stderr != "" || s.result.ExitCode != 0 {
		return s.result, nil
	}
	return pkgsandbox.ExecResult{Stdout: opts.Env["API_KEY"], ExitCode: 0}, nil
}

type fakeSecretResolver struct {
	env           map[string]string
	valid         []string
	declarable    []string
	sessionValues []string
	err           error
	uses          []string
	command       string
}

func (r *fakeSecretResolver) ResolveExecSecrets(_ context.Context, names []string, command string) (map[string]string, []string, error) {
	r.uses = append(r.uses, names...)
	r.command = command
	return r.env, r.valid, r.err
}

func (r *fakeSecretResolver) DeclarableSecretNames(context.Context) ([]string, error) {
	return r.declarable, nil
}

func (r *fakeSecretResolver) SessionSecretValues(context.Context) ([]string, error) {
	return r.sessionValues, nil
}

func TestBashDeclaredSecretInjectedOnlyIntoExecEnv(t *testing.T) {
	session := &secretCaptureSession{Session: pkgsandbox.NopSession()}
	resolver := &fakeSecretResolver{env: map[string]string{"API_KEY": "secret-value"}}
	tool := newBashTool(session, "", "", resolver)

	if _, err := tool.Execute(context.Background(), map[string]any{"command": "printf ok", "secrets": []any{"API_KEY"}}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := session.lastEnv["API_KEY"]; got != "secret-value" {
		t.Fatalf("exec env API_KEY = %q, want secret-value", got)
	}
	if _, ok := session.Policy().Env["API_KEY"]; ok {
		t.Fatal("secret leaked into session policy env")
	}
	if len(resolver.uses) != 1 || resolver.uses[0] != "API_KEY" {
		t.Fatalf("uses = %#v, want API_KEY", resolver.uses)
	}
}

func TestBashDeclaredSecretValueRedactedFromResult(t *testing.T) {
	session := &secretCaptureSession{Session: pkgsandbox.NopSession()}
	resolver := &fakeSecretResolver{env: map[string]string{"API_KEY": "secret-value"}}
	tool := newBashTool(session, "", "", resolver)

	content, err := tool.Execute(context.Background(), map[string]any{"command": "printf $API_KEY", "secrets": []any{"API_KEY"}})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.Contains(content, "secret-value") || !strings.Contains(content, "[REDACTED_SECRET]") {
		t.Fatalf("content = %q, want redacted secret", content)
	}
}

func TestBashSessionVaultSecretValueRedactedFromResult(t *testing.T) {
	session := &secretCaptureSession{Session: pkgsandbox.NopSession(), result: pkgsandbox.ExecResult{Stdout: "vault-secret-value\n", ExitCode: 0}}
	resolver := &fakeSecretResolver{sessionValues: []string{"vault-secret-value"}}
	tool := newBashTool(session, "", "", resolver)

	content, err := tool.Execute(context.Background(), map[string]any{"command": "printf $MY_SECRET"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.Contains(content, "vault-secret-value") || !strings.Contains(content, "[REDACTED_SECRET]") {
		t.Fatalf("content = %q, want redacted session vault secret", content)
	}
}

func TestBashNonDeclarableSecretFailsWithNamesOnly(t *testing.T) {
	session := &secretCaptureSession{Session: pkgsandbox.NopSession()}
	resolver := &fakeSecretResolver{valid: []string{"OK_KEY"}, err: errors.New("vault: secret \"BAD_KEY\" is not declarable")}
	tool := newBashTool(session, "", "", resolver)

	_, err := tool.Execute(context.Background(), map[string]any{"command": "printf ok", "secrets": []any{"BAD_KEY"}})
	if err == nil {
		t.Fatal("Execute succeeded, want error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "BAD_KEY") || !strings.Contains(msg, "OK_KEY") {
		t.Fatalf("error %q missing secret names", msg)
	}
	if strings.Contains(msg, "secret-value") {
		t.Fatalf("error leaked value: %q", msg)
	}
	if session.lastEnv != nil {
		t.Fatal("exec ran after non-declarable secret")
	}
}

func TestBashFailedCommandHintsUndeclaredDeclarableSecret(t *testing.T) {
	session := &secretCaptureSession{Session: pkgsandbox.NopSession(), result: pkgsandbox.ExecResult{Stderr: "missing token", ExitCode: 1}}
	resolver := &fakeSecretResolver{declarable: []string{"OTHER", "TEST_TOKEN"}}
	tool := newBashTool(session, "", "", resolver)

	content, err := tool.Execute(context.Background(), map[string]any{"command": "curl -H \"Authorization: Bearer $TEST_TOKEN\" example.test"})
	if err == nil {
		t.Fatal("Execute succeeded, want error")
	}
	if !strings.Contains(content, "hint: the command references undeclared vault secret(s): TEST_TOKEN") {
		t.Fatalf("content = %q, want undeclared secret hint", content)
	}
}

func TestBashFailedCommandWithoutSecretReferenceDoesNotHint(t *testing.T) {
	session := &secretCaptureSession{Session: pkgsandbox.NopSession(), result: pkgsandbox.ExecResult{Stderr: "nope", ExitCode: 1}}
	resolver := &fakeSecretResolver{declarable: []string{"TEST_TOKEN"}}
	tool := newBashTool(session, "", "", resolver)

	content, err := tool.Execute(context.Background(), map[string]any{"command": "false"})
	if err == nil {
		t.Fatal("Execute succeeded, want error")
	}
	if strings.Contains(content, "undeclared vault secret") {
		t.Fatalf("content = %q, want no hint", content)
	}
}

func TestBashSuccessfulCommandReferencingSecretDoesNotHint(t *testing.T) {
	session := &secretCaptureSession{Session: pkgsandbox.NopSession(), result: pkgsandbox.ExecResult{Stdout: "ok", ExitCode: 0}}
	resolver := &fakeSecretResolver{declarable: []string{"TEST_TOKEN"}}
	tool := newBashTool(session, "", "", resolver)

	content, err := tool.Execute(context.Background(), map[string]any{"command": "printf $TEST_TOKEN"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.Contains(content, "undeclared vault secret") {
		t.Fatalf("content = %q, want no hint", content)
	}
}

func TestBashFailedCommandWithDeclaredSecretDoesNotHint(t *testing.T) {
	session := &secretCaptureSession{Session: pkgsandbox.NopSession(), result: pkgsandbox.ExecResult{Stderr: "nope", ExitCode: 1}}
	resolver := &fakeSecretResolver{env: map[string]string{"TEST_TOKEN": "secret-value"}, declarable: []string{"TEST_TOKEN"}}
	tool := newBashTool(session, "", "", resolver)

	content, err := tool.Execute(context.Background(), map[string]any{"command": "printf $TEST_TOKEN", "secrets": []any{"TEST_TOKEN"}})
	if err == nil {
		t.Fatal("Execute succeeded, want error")
	}
	if strings.Contains(content, "undeclared vault secret") {
		t.Fatalf("content = %q, want no hint", content)
	}
}

func TestBashUndeclaredSecretHintChecksVariableBoundary(t *testing.T) {
	session := &secretCaptureSession{Session: pkgsandbox.NopSession(), result: pkgsandbox.ExecResult{Stderr: "nope", ExitCode: 1}}
	resolver := &fakeSecretResolver{declarable: []string{"TEST_TOKEN"}}
	tool := newBashTool(session, "", "", resolver)

	content, err := tool.Execute(context.Background(), map[string]any{"command": "printf $TEST_TOKEN_EXTRA"})
	if err == nil {
		t.Fatal("Execute succeeded, want error")
	}
	if strings.Contains(content, "undeclared vault secret") {
		t.Fatalf("content = %q, want no hint", content)
	}
}
