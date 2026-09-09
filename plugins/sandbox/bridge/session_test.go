package bridge

import (
	"context"
	"errors"
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
