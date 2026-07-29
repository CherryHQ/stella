package agent

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	agentsandbox "github.com/CherryHQ/stella/internal/agent/sandbox"
	"github.com/CherryHQ/stella/internal/config"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

type larkCLIChannelStoreStub struct {
	channels []config.Channel
	err      error
}

func (s larkCLIChannelStoreStub) ListChannelsByType(context.Context, string) ([]config.Channel, error) {
	return s.channels, s.err
}

func TestResolveLarkCLIAppConfig(t *testing.T) {
	store := larkCLIChannelStoreStub{channels: []config.Channel{
		{ID: "disabled", AgentID: "agent-1", Type: "feishu", Enabled: false, Config: `{"app_id":"ignored","app_secret":"ignored"}`},
		{ID: "other-agent", AgentID: "agent-2", Type: "feishu", Enabled: true, Config: `{"app_id":"ignored","app_secret":"ignored"}`},
		{ID: "selected", AgentID: "agent-1", Type: "feishu", Enabled: true, Config: `{"app_id":"cli_a","app_secret":"secret_a"}`},
	}}

	got, err := resolveLarkCLIAppConfig(context.Background(), store, "agent-1")
	if err != nil {
		t.Fatalf("resolveLarkCLIAppConfig: %v", err)
	}
	if got == nil || got.ChannelID != "selected" || got.AppID != "cli_a" || got.AppSecret != "secret_a" || got.Brand != "feishu" {
		t.Fatalf("config = %#v", got)
	}
}

func TestResolveLarkCLIAppConfigRejectsAmbiguousOrInvalidChannels(t *testing.T) {
	tests := []struct {
		name     string
		channels []config.Channel
	}{
		{
			name: "ambiguous",
			channels: []config.Channel{
				{ID: "one", AgentID: "agent-1", Enabled: true, Config: `{"app_id":"a","app_secret":"s"}`},
				{ID: "two", AgentID: "agent-1", Enabled: true, Config: `{"app_id":"b","app_secret":"s"}`},
			},
		},
		{name: "malformed", channels: []config.Channel{{ID: "one", AgentID: "agent-1", Enabled: true, Config: `{`}}},
		{name: "invalid brand", channels: []config.Channel{{ID: "one", AgentID: "agent-1", Enabled: true, Config: `{"app_id":"a","app_secret":"s","brand":"other"}`}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveLarkCLIAppConfig(context.Background(), larkCLIChannelStoreStub{channels: tt.channels}, "agent-1")
			if err == nil || got != nil {
				t.Fatalf("got (%#v, %v), want safe failure", got, err)
			}
			if strings.Contains(err.Error(), `"app_secret":"s"`) {
				t.Fatalf("error leaked channel config: %v", err)
			}
		})
	}
}

type closeBuffer struct {
	bytes.Buffer
}

func (*closeBuffer) Close() error { return nil }

type larkCLIProcessStub struct {
	stdin    *closeBuffer
	exitCode int
	waitErr  error
}

func (*larkCLIProcessStub) PID() int { return 1 }
func (p *larkCLIProcessStub) Wait(context.Context) (pkgsandbox.ExecResult, error) {
	return pkgsandbox.ExecResult{ExitCode: p.exitCode}, p.waitErr
}
func (p *larkCLIProcessStub) Stdin() io.WriteCloser { return p.stdin }
func (*larkCLIProcessStub) Stdout() io.ReadCloser   { return io.NopCloser(strings.NewReader("")) }
func (*larkCLIProcessStub) Stderr() io.ReadCloser   { return io.NopCloser(strings.NewReader("")) }
func (*larkCLIProcessStub) Close() error            { return nil }

type larkCLISessionStub struct {
	root      string
	policy    pkgsandbox.Policy
	requests  []pkgsandbox.ProcessRequest
	processes []*larkCLIProcessStub
	nextExit  int
	nextErr   error
}

func newLarkCLISessionStub(t *testing.T) *larkCLISessionStub {
	t.Helper()
	root := t.TempDir()
	configDir := filepath.Join(root, ".lark-cli")
	return &larkCLISessionStub{
		root: root,
		policy: pkgsandbox.Policy{
			Env: map[string]string{
				agentsandbox.LarkCLIConfigDirEnv: configDir,
				agentsandbox.LarkCLIDataDirEnv:   filepath.Join(configDir, "data"),
			},
		},
	}
}

func (s *larkCLISessionStub) Policy() pkgsandbox.Policy { return s.policy }
func (*larkCLISessionStub) Close() error                { return nil }
func (*larkCLISessionStub) Alive() bool                 { return true }
func (*larkCLISessionStub) Done() <-chan struct{}       { return make(chan struct{}) }
func (*larkCLISessionStub) Exec(context.Context, string, pkgsandbox.ExecOptions) (pkgsandbox.ExecResult, error) {
	return pkgsandbox.ExecResult{}, nil
}

func (s *larkCLISessionStub) StartProcess(_ context.Context, req pkgsandbox.ProcessRequest) (pkgsandbox.ProcessHandle, error) {
	s.requests = append(s.requests, req)
	proc := &larkCLIProcessStub{stdin: &closeBuffer{}, exitCode: s.nextExit, waitErr: s.nextErr}
	s.processes = append(s.processes, proc)
	return proc, nil
}
func (s *larkCLISessionStub) ResolvePath(path string) (string, error)      { return path, nil }
func (s *larkCLISessionStub) ResolveWritePath(path string) (string, error) { return path, nil }
func (s *larkCLISessionStub) WorkingDir() string                           { return s.root }

func TestBootstrapLarkCLIUsesStdinAndIsIdempotent(t *testing.T) {
	session := newLarkCLISessionStub(t)
	app := larkCLIAppConfig{ChannelID: "channel-1", AppID: "cli_a", AppSecret: "top-secret", Brand: "feishu"}

	if err := bootstrapLarkCLI(context.Background(), session, app); err != nil {
		t.Fatalf("bootstrapLarkCLI: %v", err)
	}
	if len(session.requests) != 2 {
		t.Fatalf("process count = %d, want config init + profile use", len(session.requests))
	}
	first := session.requests[0]
	if first.Path != "lark-cli" || !slices.Contains(first.Args, "--app-secret-stdin") {
		t.Fatalf("first request = %#v", first)
	}
	if strings.Contains(strings.Join(first.Args, " "), app.AppSecret) {
		t.Fatal("App Secret leaked into process arguments")
	}
	for key, value := range first.Env {
		if strings.Contains(key, app.AppSecret) || strings.Contains(value, app.AppSecret) {
			t.Fatal("App Secret leaked into process environment")
		}
	}
	if got := session.processes[0].stdin.String(); got != app.AppSecret+"\n" {
		t.Fatalf("stdin = %q, want App Secret plus newline", got)
	}

	if err := bootstrapLarkCLI(context.Background(), session, app); err != nil {
		t.Fatalf("idempotent bootstrapLarkCLI: %v", err)
	}
	if len(session.requests) != 2 {
		t.Fatalf("idempotent process count = %d, want 2", len(session.requests))
	}

	marker, found := readLarkCLIBinding(filepath.Join(session.policy.Env[agentsandbox.LarkCLIConfigDirEnv], larkCLIBindingMarker))
	if !found || marker.AppID != app.AppID || marker.Brand != app.Brand || marker.SecretDigest == "" {
		t.Fatalf("marker = %#v, found=%v", marker, found)
	}
	markerBytes, err := os.ReadFile(filepath.Join(session.policy.Env[agentsandbox.LarkCLIConfigDirEnv], larkCLIBindingMarker))
	if err != nil {
		t.Fatalf("ReadFile marker: %v", err)
	}
	if bytes.Contains(markerBytes, []byte(app.AppSecret)) {
		t.Fatal("App Secret leaked into binding marker")
	}
}

func TestBootstrapLarkCLISecretRotationPreservesUserState(t *testing.T) {
	session := newLarkCLISessionStub(t)
	app := larkCLIAppConfig{ChannelID: "channel-1", AppID: "cli_a", AppSecret: "secret-1", Brand: "feishu"}
	if err := bootstrapLarkCLI(context.Background(), session, app); err != nil {
		t.Fatalf("first bootstrap: %v", err)
	}
	dataFile := filepath.Join(session.policy.Env[agentsandbox.LarkCLIDataDirEnv], "token.enc")
	if err := os.MkdirAll(filepath.Dir(dataFile), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(dataFile, []byte("encrypted-token"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	app.AppSecret = "secret-2"
	if err := bootstrapLarkCLI(context.Background(), session, app); err != nil {
		t.Fatalf("rotated bootstrap: %v", err)
	}
	if _, err := os.Stat(dataFile); err != nil {
		t.Fatalf("secret-only rotation removed user state: %v", err)
	}
	if len(session.requests) != 4 {
		t.Fatalf("process count = %d, want two bootstrap sequences", len(session.requests))
	}
}

func TestBootstrapLarkCLIAppChangeResetsUserState(t *testing.T) {
	session := newLarkCLISessionStub(t)
	app := larkCLIAppConfig{ChannelID: "channel-1", AppID: "cli_a", AppSecret: "secret-1", Brand: "feishu"}
	if err := bootstrapLarkCLI(context.Background(), session, app); err != nil {
		t.Fatalf("first bootstrap: %v", err)
	}
	dataFile := filepath.Join(session.policy.Env[agentsandbox.LarkCLIDataDirEnv], "token.enc")
	if err := os.MkdirAll(filepath.Dir(dataFile), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(dataFile, []byte("encrypted-token"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	app.AppID = "cli_b"
	if err := bootstrapLarkCLI(context.Background(), session, app); err != nil {
		t.Fatalf("changed app bootstrap: %v", err)
	}
	if _, err := os.Stat(dataFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old user state still exists: %v", err)
	}
}

func TestBootstrapLarkCLIFailureDoesNotLeakSecretOrWriteMarker(t *testing.T) {
	session := newLarkCLISessionStub(t)
	session.nextExit = 1
	app := larkCLIAppConfig{ChannelID: "channel-1", AppID: "cli_a", AppSecret: "top-secret", Brand: "feishu"}

	err := bootstrapLarkCLI(context.Background(), session, app)
	if err == nil {
		t.Fatal("bootstrapLarkCLI succeeded, want failure")
	}
	if strings.Contains(err.Error(), app.AppSecret) {
		t.Fatalf("error leaked App Secret: %v", err)
	}
	markerPath := filepath.Join(session.policy.Env[agentsandbox.LarkCLIConfigDirEnv], larkCLIBindingMarker)
	if _, statErr := os.Stat(markerPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("marker exists after failed bootstrap: %v", statErr)
	}
}
