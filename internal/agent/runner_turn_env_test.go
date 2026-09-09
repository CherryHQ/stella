package agent

import (
	"context"
	"errors"
	"maps"
	"testing"

	"github.com/CherryHQ/stella/internal/agent/sandbox"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

type selectingSession struct {
	*fakeSession
	selected bool
}

type renderingSession struct {
	*fakeSession
	renderCalls int
	home        string
}

func (s *renderingSession) RenderEnv(_ context.Context, env map[string]string) (map[string]string, error) {
	s.renderCalls++
	if _, ok := env["OLD_SECRET"]; ok {
		return nil, errors.New("stale environment leaked into renderer")
	}
	home := s.home
	if home == "" {
		home = "/sandbox/home"
	}
	rendered := map[string]string{"HOME": home, "PATH": "/sandbox/bin"}
	maps.Copy(rendered, env)
	return rendered, nil
}

func (s *selectingSession) SelectFileView(context.Context) (pkgsandbox.FileView, error) {
	s.selected = true
	return pkgsandbox.FileView{Policy: s.policy, Files: s.Files()}, nil
}

func TestTurnEnvSessionReplacesEveryProcessRequest(t *testing.T) {
	raw := &renderingSession{fakeSession: &fakeSession{alive: true, policy: pkgsandbox.Policy{Env: map[string]string{"OLD_SECRET": "stale"}}}}
	wrapped := turnEnvSession{Session: raw}
	ctx := sandbox.WithTurnEnv(t.Context(), map[string]string{"BASE": "turn", "STALE": "gone"})
	if _, err := wrapped.Exec(ctx, "true", pkgsandbox.ExecOptions{Env: map[string]string{"BASE": "explicit", "EXTRA": "yes"}}); err != nil {
		t.Fatal(err)
	}
	if raw.renderCalls != 1 || raw.lastExec.EnvMode != pkgsandbox.EnvReplace || raw.lastExec.Env["HOME"] != "/sandbox/home" || raw.lastExec.Env["PATH"] != "/sandbox/bin" || raw.lastExec.Env["STALE"] != "gone" || raw.lastExec.Env["OLD_SECRET"] != "" || raw.lastExec.Env["BASE"] != "explicit" || raw.lastExec.Env["EXTRA"] != "yes" {
		t.Fatalf("exec env = %#v mode=%v", raw.lastExec.Env, raw.lastExec.EnvMode)
	}
	if _, err := wrapped.StartProcess(ctx, pkgsandbox.ProcessRequest{Env: map[string]string{"CHILD": "yes"}}); err != nil {
		t.Fatal(err)
	}
	if raw.lastProcess.EnvMode != pkgsandbox.EnvReplace || raw.lastProcess.Env["BASE"] != "turn" || raw.lastProcess.Env["CHILD"] != "yes" {
		t.Fatalf("process env = %#v mode=%v", raw.lastProcess.Env, raw.lastProcess.EnvMode)
	}
}

func TestTurnEnvSessionUsesOneResilientGenerationForRenderAndExec(t *testing.T) {
	initial := &renderingSession{fakeSession: &fakeSession{alive: false}}
	createCalls := 0
	var recreated *renderingSession
	resilient := pkgsandbox.NewResilientSession(initial, func(context.Context) (pkgsandbox.Session, error) {
		createCalls++
		recreated = &renderingSession{fakeSession: &fakeSession{alive: true}, home: "/new/home"}
		return recreated, nil
	})
	wrapped := turnEnvSession{Session: resilient}
	ctx := sandbox.WithTurnEnv(t.Context(), map[string]string{"TURN": "current"})
	if _, err := wrapped.Exec(ctx, "true", pkgsandbox.ExecOptions{}); err != nil {
		t.Fatal(err)
	}
	if createCalls != 1 || recreated == nil || recreated.renderCalls != 1 || recreated.lastExec.Env["HOME"] != "/new/home" || recreated.lastExec.Env["TURN"] != "current" {
		t.Fatalf("generation calls=%d render=%d env=%#v", createCalls, recreated.renderCalls, recreated.lastExec.Env)
	}
	if _, err := wrapped.Exec(ctx, "true", pkgsandbox.ExecOptions{}); err != nil {
		t.Fatal(err)
	}
	if createCalls != 1 || recreated.renderCalls != 2 {
		t.Fatalf("same generation was recreated or skipped rendering: create=%d render=%d", createCalls, recreated.renderCalls)
	}
}

func TestTurnEnvSessionForwardsCoherentFileSelection(t *testing.T) {
	raw := &selectingSession{fakeSession: &fakeSession{alive: true}}
	wrapped := turnEnvSession{Session: raw}
	if _, err := pkgsandbox.SelectFileView(t.Context(), wrapped); err != nil {
		t.Fatal(err)
	}
	if !raw.selected {
		t.Fatal("wrapped session bypassed retained-session file selection")
	}
}
