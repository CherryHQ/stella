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
	"github.com/moby/moby/api/types/system"
	mobyclient "github.com/moby/moby/client"

	"github.com/CherryHQ/stella/plugins/sandbox/docker/dockerclient"
)

// fakePreflightAPI is a minimal dockerclient.API stub that satisfies the
// interface while letting each test override the handful of methods Preflight
// actually calls.
type fakePreflightAPI struct {
	noopAPI

	versionFn func() (mobyclient.ServerVersionResult, error)
	infoFn    func() (mobyclient.SystemInfoResult, error)
	inspectFn func(image string) (mobyclient.ImageInspectResult, error)
	pullFn    func(image string) (mobyclient.ImagePullResponse, error)
}

func (f *fakePreflightAPI) Info(context.Context, mobyclient.InfoOptions) (mobyclient.SystemInfoResult, error) {
	if f.infoFn == nil {
		return supportedSystemInfo(), nil
	}
	return f.infoFn()
}

func supportedSystemInfo() mobyclient.SystemInfoResult {
	return mobyclient.SystemInfoResult{Info: system.Info{
		MemoryLimit:    true,
		CPUCfsPeriod:   true,
		CPUCfsQuota:    true,
		PidsLimit:      true,
		DefaultRuntime: "runc",
		Runtimes:       map[string]system.RuntimeWithStatus{"runc": {}},
	}}
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

func TestPreflightRuntime(t *testing.T) {
	t.Run("registered", func(t *testing.T) {
		api := &fakePreflightAPI{
			infoFn: func() (mobyclient.SystemInfoResult, error) {
				info := supportedSystemInfo()
				info.Info.Runtimes = map[string]system.RuntimeWithStatus{"runsc": {}}
				return info, nil
			},
			inspectFn: func(string) (mobyclient.ImageInspectResult, error) {
				return mobyclient.ImageInspectResult{}, nil
			},
		}
		client := dockerclient.NewWithAPI(api)
		cfg := PreflightConfig{Docker: Config{Image: "sandbox:test", Runtime: "runsc"}}
		if err := preflightWithClient(context.Background(), cfg, client); err != nil {
			t.Fatalf("registered runtime rejected: %v", err)
		}
	})

	t.Run("missing fails closed", func(t *testing.T) {
		api := &fakePreflightAPI{}
		client := dockerclient.NewWithAPI(api)
		cfg := PreflightConfig{Docker: Config{Image: "sandbox:test", Runtime: "runsc"}}
		err := preflightWithClient(context.Background(), cfg, client)
		if err == nil || !strings.Contains(err.Error(), `runtime "runsc"`) {
			t.Fatalf("missing runtime error = %v", err)
		}
	})

	t.Run("runtime disabling cgroups fails closed", func(t *testing.T) {
		api := &fakePreflightAPI{infoFn: func() (mobyclient.SystemInfoResult, error) {
			info := supportedSystemInfo()
			info.Info.Runtimes = map[string]system.RuntimeWithStatus{
				"runsc": {Runtime: system.Runtime{Args: []string{"--ignore-cgroups"}}},
			}
			return info, nil
		}}
		client := dockerclient.NewWithAPI(api)
		err := preflightWithClient(context.Background(), PreflightConfig{Docker: Config{Image: "sandbox:test", Runtime: "runsc"}}, client)
		if err == nil || !strings.Contains(err.Error(), "disables Stella sandbox resource limits") {
			t.Fatalf("unsafe runtime error = %v", err)
		}
	})

	t.Run("default runtime disabling cgroups fails closed", func(t *testing.T) {
		api := &fakePreflightAPI{infoFn: func() (mobyclient.SystemInfoResult, error) {
			info := supportedSystemInfo()
			info.Info.DefaultRuntime = "runsc"
			info.Info.Runtimes["runsc"] = system.RuntimeWithStatus{Runtime: system.Runtime{Args: []string{"--ignore-cgroups=1"}}}
			return info, nil
		}}
		client := dockerclient.NewWithAPI(api)
		err := preflightWithClient(context.Background(), PreflightConfig{Docker: Config{Image: "sandbox:test"}}, client)
		if err == nil || !strings.Contains(err.Error(), `runtime "runsc"`) || !strings.Contains(err.Error(), "disables Stella sandbox resource limits") {
			t.Fatalf("unsafe default runtime error = %v", err)
		}
	})
}

func TestPreflightResourceLimits(t *testing.T) {
	t.Run("rootless with all limits passes", func(t *testing.T) {
		api := &fakePreflightAPI{
			infoFn: func() (mobyclient.SystemInfoResult, error) {
				info := supportedSystemInfo()
				info.Info.SecurityOptions = []string{"name=rootless,param=value"}
				info.Info.CgroupDriver = "systemd"
				return info, nil
			},
			inspectFn: func(string) (mobyclient.ImageInspectResult, error) {
				return mobyclient.ImageInspectResult{}, nil
			},
		}
		client := dockerclient.NewWithAPI(api)
		if err := preflightWithClient(context.Background(), PreflightConfig{Docker: Config{Image: "sandbox:test"}}, client); err != nil {
			t.Fatalf("supported rootless daemon rejected: %v", err)
		}
	})

	t.Run("missing limits fail closed", func(t *testing.T) {
		api := &fakePreflightAPI{
			infoFn: func() (mobyclient.SystemInfoResult, error) {
				return mobyclient.SystemInfoResult{Info: system.Info{
					SecurityOptions: []string{"name=rootless"},
					CgroupDriver:    "none",
					MemoryLimit:     true,
				}}, nil
			},
		}
		client := dockerclient.NewWithAPI(api)
		err := preflightWithClient(context.Background(), PreflightConfig{Docker: Config{Image: "sandbox:test"}}, client)
		if err == nil || !strings.Contains(err.Error(), "CPU quota, PID") {
			t.Fatalf("rootless daemon without cgroups error = %v", err)
		}
	})

	t.Run("userns remap fails closed", func(t *testing.T) {
		api := &fakePreflightAPI{infoFn: func() (mobyclient.SystemInfoResult, error) {
			info := supportedSystemInfo()
			info.Info.SecurityOptions = []string{"name=userns"}
			return info, nil
		}}
		client := dockerclient.NewWithAPI(api)
		err := preflightWithClient(context.Background(), PreflightConfig{Docker: Config{Image: "sandbox:test"}}, client)
		if err == nil || !strings.Contains(err.Error(), "userns-remap is unsupported") {
			t.Fatalf("userns-remap error = %v", err)
		}
	})
}

func TestUnsafeRuntimeResourceArg(t *testing.T) {
	for _, tc := range []struct {
		args   []string
		unsafe bool
	}{
		{args: []string{"--ignore-cgroups"}, unsafe: true},
		{args: []string{"--ignore-cgroups=TRUE"}, unsafe: true},
		{args: []string{"--ignore-cgroups=1"}, unsafe: true},
		{args: []string{"--ignore-cgroups=t"}, unsafe: true},
		{args: []string{"--ignore-cgroups=maybe"}, unsafe: true},
		{args: []string{"--ignore-cgroups=false"}, unsafe: false},
		{args: []string{"--platform=systrap"}, unsafe: false},
	} {
		_, got := unsafeRuntimeResourceArg(tc.args)
		if got != tc.unsafe {
			t.Errorf("unsafeRuntimeResourceArg(%v) = %v, want %v", tc.args, got, tc.unsafe)
		}
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
