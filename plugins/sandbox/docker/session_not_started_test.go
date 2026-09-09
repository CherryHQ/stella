package docker

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	mobyclient "github.com/moby/moby/client"

	sandboxpkg "github.com/CherryHQ/stella/pkg/sandbox"
	"github.com/CherryHQ/stella/plugins/sandbox/docker/dockerclient"
	"github.com/CherryHQ/stella/plugins/sandbox/internal/sessionfs"
)

type processClassificationAPI struct {
	noopAPI
	execCreates   atomic.Int32
	execCreateErr error
	execAttachErr error
	exitCode      int
}

func (a *processClassificationAPI) ExecCreate(context.Context, string, mobyclient.ExecCreateOptions) (mobyclient.ExecCreateResult, error) {
	a.execCreates.Add(1)
	if a.execCreateErr != nil {
		return mobyclient.ExecCreateResult{}, a.execCreateErr
	}
	return mobyclient.ExecCreateResult{ID: "exec-1"}, nil
}

func (a *processClassificationAPI) ExecInspect(context.Context, string, mobyclient.ExecInspectOptions) (mobyclient.ExecInspectResult, error) {
	return mobyclient.ExecInspectResult{ExitCode: a.exitCode}, nil
}

func (a *processClassificationAPI) ExecAttach(context.Context, string, mobyclient.ExecAttachOptions) (mobyclient.ExecAttachResult, error) {
	if a.execAttachErr != nil {
		return mobyclient.ExecAttachResult{}, a.execAttachErr
	}
	return a.noopAPI.ExecAttach(context.Background(), "", mobyclient.ExecAttachOptions{})
}

func newProcessClassificationSession(t *testing.T, api *processClassificationAPI) *dockerSession {
	t.Helper()
	root := t.TempDir()
	resolver, err := sessionfs.NewResolver("/workspace", []sessionfs.Mount{{
		HostPath:    root,
		SandboxPath: "/workspace",
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resolver.Close() })

	session := &dockerSession{
		client:      dockerclient.NewWithAPI(api),
		containerID: "container-1",
		policy: sandboxpkg.Policy{Filesystem: sandboxpkg.FilesystemPolicy{
			WorkingDir: "/workspace",
		}},
		mountTable: []dockerclient.Mount{{
			HostPath:      root,
			ContainerPath: "/workspace",
		}},
		resolver: resolver,
		done:     make(chan struct{}),
	}
	session.host = &dockerHost{session: session}
	return session
}

func TestDockerExecPreflightFailuresAreNotStarted(t *testing.T) {
	tests := []struct {
		name string
		call func(*dockerSession) error
	}{
		{
			name: "invalid cwd",
			call: func(session *dockerSession) error {
				_, err := session.Exec(t.Context(), "true", sandboxpkg.ExecOptions{Cwd: "/missing"})
				return err
			},
		},
		{
			name: "cancelled context",
			call: func(session *dockerSession) error {
				ctx, cancel := context.WithCancel(t.Context())
				cancel()
				_, err := session.Exec(ctx, "true", sandboxpkg.ExecOptions{})
				return err
			},
		},
		{
			name: "closed session",
			call: func(session *dockerSession) error {
				session.closed = true
				_, err := session.Exec(t.Context(), "true", sandboxpkg.ExecOptions{})
				return err
			},
		},
		{
			name: "invalid environment mode",
			call: func(session *dockerSession) error {
				_, err := session.Exec(t.Context(), "true", sandboxpkg.ExecOptions{EnvMode: sandboxpkg.EnvMode(255)})
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			api := &processClassificationAPI{}
			session := newProcessClassificationSession(t, api)
			if err := test.call(session); err == nil || !errors.Is(err, sandboxpkg.ErrNotStarted) {
				t.Fatalf("error = %v, want NotStartedError", err)
			}
			if got := api.execCreates.Load(); got != 0 {
				t.Fatalf("ExecCreate calls = %d, want 0 for preflight failure", got)
			}
		})
	}
}

func TestDockerStartProcessPreflightFailuresAreNotStarted(t *testing.T) {
	tests := []struct {
		name string
		call func(*dockerSession) error
	}{
		{
			name: "invalid cwd",
			call: func(session *dockerSession) error {
				_, err := session.StartProcess(t.Context(), sandboxpkg.ProcessRequest{Path: "/bin/true", Cwd: "/missing"})
				return err
			},
		},
		{
			name: "cancelled context",
			call: func(session *dockerSession) error {
				ctx, cancel := context.WithCancel(t.Context())
				cancel()
				_, err := session.StartProcess(ctx, sandboxpkg.ProcessRequest{Path: "/bin/true"})
				return err
			},
		},
		{
			name: "empty path",
			call: func(session *dockerSession) error {
				_, err := session.StartProcess(t.Context(), sandboxpkg.ProcessRequest{})
				return err
			},
		},
		{
			name: "invalid environment mode",
			call: func(session *dockerSession) error {
				_, err := session.StartProcess(t.Context(), sandboxpkg.ProcessRequest{Path: "/bin/true", EnvMode: sandboxpkg.EnvMode(255)})
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			api := &processClassificationAPI{}
			session := newProcessClassificationSession(t, api)
			if err := test.call(session); err == nil || !errors.Is(err, sandboxpkg.ErrNotStarted) {
				t.Fatalf("error = %v, want NotStartedError", err)
			}
			if got := api.execCreates.Load(); got != 0 {
				t.Fatalf("ExecCreate calls = %d, want 0 for preflight failure", got)
			}
		})
	}
}

func TestDockerExecCreateErrorsRemainUnknown(t *testing.T) {
	api := &processClassificationAPI{execCreateErr: errors.New("preflight bad request")}
	session := newProcessClassificationSession(t, api)
	_, err := session.Exec(t.Context(), "true", sandboxpkg.ExecOptions{})
	if err == nil {
		t.Fatal("expected ExecCreate error")
	}
	if errors.Is(err, sandboxpkg.ErrNotStarted) {
		t.Fatalf("ExecCreate error was classified as NotStartedError: %v", err)
	}
	if got := api.execCreates.Load(); got != 1 {
		t.Fatalf("ExecCreate calls = %d, want 1", got)
	}
	_, err = session.StartProcess(t.Context(), sandboxpkg.ProcessRequest{Path: "/bin/true"})
	if err == nil {
		t.Fatal("expected StartProcess ExecCreate error")
	}
	if errors.Is(err, sandboxpkg.ErrNotStarted) {
		t.Fatalf("StartProcess ExecCreate error was classified as NotStartedError: %v", err)
	}
	if got := api.execCreates.Load(); got != 2 {
		t.Fatalf("ExecCreate calls = %d, want 2", got)
	}
}

func TestDockerExecAttachErrorsRemainUnknown(t *testing.T) {
	api := &processClassificationAPI{execAttachErr: errors.New("attach outcome is unknown")}
	session := newProcessClassificationSession(t, api)
	_, err := session.Exec(t.Context(), "true", sandboxpkg.ExecOptions{})
	if err == nil {
		t.Fatal("expected ExecAttach error")
	}
	if errors.Is(err, sandboxpkg.ErrNotStarted) {
		t.Fatalf("ExecAttach error was classified as NotStartedError: %v", err)
	}
	_, err = session.StartProcess(t.Context(), sandboxpkg.ProcessRequest{Path: "/bin/true"})
	if err == nil {
		t.Fatal("expected StartProcess ExecAttach error")
	}
	if errors.Is(err, sandboxpkg.ErrNotStarted) {
		t.Fatalf("StartProcess ExecAttach error was classified as NotStartedError: %v", err)
	}
}

func TestDockerNormalNonZeroExitIsKnown(t *testing.T) {
	api := &processClassificationAPI{exitCode: 17}
	session := newProcessClassificationSession(t, api)
	result, err := session.Exec(t.Context(), "exit 17", sandboxpkg.ExecOptions{})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if result.ExitCode != 17 {
		t.Fatalf("exit code = %d, want 17", result.ExitCode)
	}
	process, err := session.StartProcess(t.Context(), sandboxpkg.ProcessRequest{Path: "/bin/true"})
	if err != nil {
		t.Fatalf("StartProcess: %v", err)
	}
	defer func() { _ = process.Close() }()
	result, err = process.Wait(t.Context())
	if err != nil {
		t.Fatalf("StartProcess Wait: %v", err)
	}
	if result.ExitCode != 17 {
		t.Fatalf("StartProcess exit code = %d, want 17", result.ExitCode)
	}
}

func TestDockerRenderEnvPreflightErrorsAreNotStarted(t *testing.T) {
	session := &dockerSession{filesystemView: sandboxpkg.FilesystemView{Home: "/workspace", TempDir: "/tmp"}}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := session.RenderEnv(ctx, map[string]string{}); err == nil || !errors.Is(err, sandboxpkg.ErrNotStarted) {
		t.Fatalf("cancelled RenderEnv error = %v, want NotStartedError", err)
	}

	invalidView := &dockerSession{}
	if _, err := invalidView.RenderEnv(t.Context(), map[string]string{}); err == nil || !errors.Is(err, sandboxpkg.ErrNotStarted) {
		t.Fatalf("invalid RenderEnv input error = %v, want NotStartedError", err)
	}
}
