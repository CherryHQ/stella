package dockerclient

import (
	"context"
	"errors"
	"testing"

	"github.com/moby/moby/api/types/system"
	mobyclient "github.com/moby/moby/client"
)

type runtimeAPI struct {
	API
	result mobyclient.SystemInfoResult
	err    error
}

func (f runtimeAPI) Info(context.Context, mobyclient.InfoOptions) (mobyclient.SystemInfoResult, error) {
	return f.result, f.err
}

func TestRuntime(t *testing.T) {
	client := NewWithAPI(runtimeAPI{result: mobyclient.SystemInfoResult{
		Info: system.Info{DefaultRuntime: "runc", Runtimes: map[string]system.RuntimeWithStatus{
			"runc":  {},
			"runsc": {Runtime: system.Runtime{Args: []string{"--platform=systrap"}}},
		}},
	}})

	for _, tc := range []struct {
		name string
		want bool
	}{{"", true}, {"runsc", true}, {"kata", false}} {
		info, got, err := client.Runtime(context.Background(), tc.name)
		if err != nil {
			t.Fatalf("Runtime(%q): %v", tc.name, err)
		}
		if got != tc.want {
			t.Errorf("Runtime(%q) registered = %v, want %v", tc.name, got, tc.want)
		}
		if tc.name == "runsc" && len(info.Args) != 1 {
			t.Errorf("Runtime(runsc) args = %v", info.Args)
		}
		if tc.name == "" && info.Name != "runc" {
			t.Errorf("Runtime(default) name = %q, want runc", info.Name)
		}
	}
}

func TestRuntimeInfoError(t *testing.T) {
	client := NewWithAPI(runtimeAPI{err: errors.New("daemon failed")})
	if _, _, err := client.Runtime(context.Background(), "runsc"); err == nil {
		t.Fatal("Runtime succeeded despite daemon info error")
	}
}

func TestSecurity(t *testing.T) {
	client := NewWithAPI(runtimeAPI{result: mobyclient.SystemInfoResult{
		Info: system.Info{
			SecurityOptions: []string{"name=seccomp,profile=builtin", "name=rootless,param=value"},
			CgroupDriver:    "systemd",
			MemoryLimit:     true,
			CPUCfsPeriod:    true,
			CPUCfsQuota:     true,
			PidsLimit:       true,
		},
	}})

	got, err := client.Security(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !got.Rootless || got.CgroupDriver != "systemd" || !got.MemoryLimit || !got.CPUCfsPeriod || !got.CPUCfsQuota || !got.PidsLimit {
		t.Fatalf("Security = %+v, want rootless systemd", got)
	}
}

func TestSecurityRootful(t *testing.T) {
	client := NewWithAPI(runtimeAPI{result: mobyclient.SystemInfoResult{
		Info: system.Info{SecurityOptions: []string{"name=seccomp,profile=builtin", "name=userns,mode=remap"}, CgroupDriver: "cgroupfs"},
	}})

	got, err := client.Security(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Rootless || !got.UserNamespace || got.CgroupDriver != "cgroupfs" {
		t.Fatalf("Security = %+v, want rootful userns-remap cgroupfs", got)
	}
}
