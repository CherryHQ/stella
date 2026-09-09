package bridge

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"runtime"
	"strings"
	"testing"

	sandboxpkg "github.com/CherryHQ/stella/pkg/sandbox"
)

func TestSessionRenderEnvUsesBindingCoordinates(t *testing.T) {
	s := &session{
		filesystemView: sandboxpkg.FilesystemView{Home: "/task/home", TempDir: "/task/tmp"},
		runnerPath:     "/stella/bin:/usr/bin:/bin",
		done:           make(chan struct{}),
	}
	rendered, err := s.RenderEnv(context.Background(), map[string]string{
		"PATH": "/host/stale",
	})
	if err != nil {
		t.Fatalf("RenderEnv: %v", err)
	}
	if rendered[sandboxpkg.EnvHome] != "/task/home" || rendered[sandboxpkg.EnvTempDir] != "/task/tmp" {
		t.Fatalf("binding filesystem coordinates = %#v", rendered)
	}
	if rendered["PATH"] != "/stella/bin:/usr/bin:/bin" || rendered[sandboxpkg.EnvRunnerPath] != rendered["PATH"] {
		t.Fatalf("binding PATH = %#v", rendered)
	}
	if _, ok := rendered["OLD_TOKEN"]; ok {
		t.Fatal("renderer resurrected a removed old token")
	}
}

func TestSessionResourceIdentityReportsExternalBindingWithoutTerminationProof(t *testing.T) {
	s := &session{client: &client{socket: "/tmp/bridge.sock", nonce: "nonce-1"}, done: make(chan struct{})}
	identity, err := s.ResourceIdentity(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if identity.Backend != "bridge" || identity.Authority != "/tmp/bridge.sock" || identity.Ref != "nonce-1" {
		t.Fatalf("external resource identity = %+v", identity)
	}
	if _, ok := any(s).(sandboxpkg.ResourceController); ok {
		t.Fatal("bridge session must not claim a local resource controller")
	}
}

func TestExecCanceledBeforeBridgeDialIsNotStarted(t *testing.T) {
	s := &session{
		client: &client{socket: "/does/not/exist", nonce: "nonce-1"},
		policy: sandboxpkg.Policy{Filesystem: sandboxpkg.FilesystemPolicy{WorkingDir: "/app"}},
		done:   make(chan struct{}),
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := s.Exec(ctx, "true", sandboxpkg.ExecOptions{})
	if err == nil || !errors.Is(err, sandboxpkg.ErrNotStarted) {
		t.Fatalf("canceled bridge Exec error = %v, want NotStartedError", err)
	}
	_, err = s.Exec(t.Context(), "true", sandboxpkg.ExecOptions{EnvMode: sandboxpkg.EnvMode(99)})
	if err == nil || !errors.Is(err, sandboxpkg.ErrNotStarted) {
		t.Fatalf("invalid bridge environment mode error = %v, want NotStartedError", err)
	}
}

func TestExecBridgeTransportFailureRemainsUnknown(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bridge transport uses Unix sockets")
	}
	socket := fmt.Sprintf("/tmp/stella-bridge-%d.sock", os.Getpid())
	_ = os.Remove(socket)
	defer func() { _ = os.Remove(socket) }()
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	accepted := make(chan struct{})
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr == nil {
			_ = conn.Close()
		}
		close(accepted)
	}()
	s := &session{
		client: &client{socket: socket, nonce: "nonce-1"},
		policy: sandboxpkg.Policy{Filesystem: sandboxpkg.FilesystemPolicy{WorkingDir: "/app"}},
		done:   make(chan struct{}),
	}
	_, err = s.Exec(t.Context(), "true", sandboxpkg.ExecOptions{})
	if err == nil || errors.Is(err, sandboxpkg.ErrNotStarted) {
		t.Fatalf("bridge transport error = %v, want uncertain result", err)
	}
	<-accepted
}

func TestStartProcessUnsupportedIsNotStarted(t *testing.T) {
	s := &session{done: make(chan struct{})}
	_, err := s.StartProcess(t.Context(), sandboxpkg.ProcessRequest{Path: "/bin/true"})
	if err == nil || !errors.Is(err, sandboxpkg.ErrNotStarted) {
		t.Fatalf("unsupported bridge StartProcess error = %v, want NotStartedError", err)
	}
}

func TestMapFileCallErrorMapsStructuredTooLargeResponse(t *testing.T) {
	original := errors.New("bridge: read_file failed")
	err := mapFileCallError(response{
		Code:  codeTooLarge,
		Size:  51_066_691,
		Limit: 33_554_432,
	}, request{Op: "read_file", Path: "/app/input.csv"}, original)

	var tooLarge *sandboxpkg.FileTooLargeError
	if !errors.As(err, &tooLarge) {
		t.Fatalf("error = %v, want FileTooLargeError", err)
	}
	if tooLarge.Size != 51_066_691 || tooLarge.Limit != 33_554_432 {
		t.Fatalf("FileTooLargeError = %+v", tooLarge)
	}
	if got := err.Error(); got == "" || strings.Contains(got, "/app/input.csv") {
		t.Fatalf("generic error leaked bridge path: %q", got)
	}
}

func TestMapFileCallErrorLeavesUnstructuredTooLargeResponseGeneric(t *testing.T) {
	original := errors.New("bridge: project: request exceeds cap")
	err := mapFileCallError(response{Code: codeTooLarge}, request{Op: "project"}, original)
	if !errors.Is(err, original) {
		t.Fatalf("error = %v, want original generic transport error", err)
	}
}
