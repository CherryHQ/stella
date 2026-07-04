package tools

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/pkg/sandbox"
)

type secretCaptureSession struct {
	sandbox.Session
	lastEnv map[string]string
}

func (s *secretCaptureSession) Exec(_ context.Context, _ string, opts sandbox.ExecOptions) (sandbox.ExecResult, error) {
	s.lastEnv = opts.Env
	return sandbox.ExecResult{Stdout: opts.Env["API_KEY"], ExitCode: 0}, nil
}

type fakeSecretResolver struct {
	env     map[string]string
	valid   []string
	err     error
	uses    []string
	command string
}

func (r *fakeSecretResolver) ResolveExecSecrets(_ context.Context, names []string, command string) (map[string]string, []string, error) {
	r.uses = append(r.uses, names...)
	r.command = command
	return r.env, r.valid, r.err
}

func TestBashDeclaredSecretInjectedOnlyIntoExecEnv(t *testing.T) {
	session := &secretCaptureSession{Session: sandbox.NopSession()}
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
	session := &secretCaptureSession{Session: sandbox.NopSession()}
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

func TestBashNonDeclarableSecretFailsWithNamesOnly(t *testing.T) {
	session := &secretCaptureSession{Session: sandbox.NopSession()}
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
