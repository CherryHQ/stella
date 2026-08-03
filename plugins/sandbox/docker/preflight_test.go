package docker

import (
	"context"
	"errors"
	"io"
	"iter"
	"strings"
	"testing"

	"github.com/containerd/errdefs"
	jsonstream "github.com/moby/moby/api/types/jsonstream"
	mobyclient "github.com/moby/moby/client"

	"github.com/CherryHQ/stella/plugins/sandbox/docker/dockerclient"
)

// fakePreflightAPI is a minimal dockerclient.API stub that satisfies the
// interface while letting each test override the handful of methods Preflight
// actually calls.
type fakePreflightAPI struct {
	noopAPI

	versionFn func() (mobyclient.ServerVersionResult, error)
	inspectFn func(image string) (mobyclient.ImageInspectResult, error)
	pullFn    func(image string) (mobyclient.ImagePullResponse, error)
}

func (f *fakePreflightAPI) ServerVersion(context.Context, mobyclient.ServerVersionOptions) (mobyclient.ServerVersionResult, error) {
	if f.versionFn == nil {
		return mobyclient.ServerVersionResult{APIVersion: "1.43"}, nil
	}
	return f.versionFn()
}

func (f *fakePreflightAPI) ImageInspect(_ context.Context, image string, _ ...mobyclient.ImageInspectOption) (mobyclient.ImageInspectResult, error) {
	if f.inspectFn == nil {
		return mobyclient.ImageInspectResult{}, errdefs.ErrNotFound
	}
	return f.inspectFn(image)
}

func (f *fakePreflightAPI) ImagePull(_ context.Context, image string, _ mobyclient.ImagePullOptions) (mobyclient.ImagePullResponse, error) {
	if f.pullFn == nil {
		return noopPullResponse{}, nil
	}
	return f.pullFn(image)
}

// noopPullResponse satisfies mobyclient.ImagePullResponse with an empty body.
type noopPullResponse struct{}

func (noopPullResponse) Read(p []byte) (int, error) { return 0, io.EOF }
func (noopPullResponse) Close() error               { return nil }
func (noopPullResponse) Wait(context.Context) error { return nil }
func (noopPullResponse) JSONMessages(context.Context) iter.Seq2[jsonstream.Message, error] {
	return func(yield func(jsonstream.Message, error) bool) {}
}

func TestPreflightImageMissingRequired(t *testing.T) {
	api := &fakePreflightAPI{}
	client := dockerclient.NewWithAPI(api)

	err := preflightWithClient(context.Background(), PreflightConfig{}, client)
	if err == nil {
		t.Fatal("expected error when Image is empty")
	}
	if !strings.Contains(err.Error(), "Image is required") {
		t.Errorf("expected error about missing Image, got: %v", err)
	}
}

func TestPreflightImageExists(t *testing.T) {
	api := &fakePreflightAPI{
		inspectFn: func(string) (mobyclient.ImageInspectResult, error) {
			return mobyclient.ImageInspectResult{}, nil
		},
	}
	client := dockerclient.NewWithAPI(api)

	cfg := PreflightConfig{Docker: Config{Image: "alpine:3.20"}}
	if err := preflightWithClient(context.Background(), cfg, client); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestPreflightImageMissingPulls(t *testing.T) {
	pulled := false
	api := &fakePreflightAPI{
		pullFn: func(string) (mobyclient.ImagePullResponse, error) {
			pulled = true
			return noopPullResponse{}, nil
		},
	}
	client := dockerclient.NewWithAPI(api)

	cfg := PreflightConfig{Docker: Config{Image: "myimage:latest"}}
	if err := preflightWithClient(context.Background(), cfg, client); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !pulled {
		t.Error("expected ImagePull to be invoked when image is missing")
	}
}

func TestPreflightDaemonUnreachable(t *testing.T) {
	api := &fakePreflightAPI{
		versionFn: func() (mobyclient.ServerVersionResult, error) {
			return mobyclient.ServerVersionResult{}, errors.New("cannot connect to daemon")
		},
	}
	client := dockerclient.NewWithAPI(api)

	cfg := PreflightConfig{Docker: Config{Image: "alpine:3.20"}}
	err := preflightWithClient(context.Background(), cfg, client)
	if err == nil {
		t.Fatal("expected error when daemon unreachable")
	}
	if !strings.Contains(err.Error(), "daemon not reachable") {
		t.Errorf("expected 'daemon not reachable' in error, got: %v", err)
	}
}

func TestPreflightRejectsBuiltinBundleRevisionMismatch(t *testing.T) {
	api := &fakePreflightAPI{inspectFn: func(string) (mobyclient.ImageInspectResult, error) { return mobyclient.ImageInspectResult{}, nil }}
	client := dockerclient.NewWithAPI(api)
	err := preflightWithClient(context.Background(), PreflightConfig{Docker: Config{Image: "sandbox:test", ExpectedBundleRevision: "expected"}}, client)
	if err == nil || !strings.Contains(err.Error(), "expected expected, image has ") || !strings.Contains(err.Error(), "mise run sandbox:docker:build") {
		t.Fatalf("preflight mismatch error = %v", err)
	}
}
