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

func TestRuntimeAvailable(t *testing.T) {
	client := NewWithAPI(runtimeAPI{result: mobyclient.SystemInfoResult{
		Info: system.Info{Runtimes: map[string]system.RuntimeWithStatus{"runc": {}, "runsc": {}}},
	}})

	for _, tc := range []struct {
		name string
		want bool
	}{{"", true}, {"runsc", true}, {"kata", false}} {
		got, err := client.RuntimeAvailable(context.Background(), tc.name)
		if err != nil {
			t.Fatalf("RuntimeAvailable(%q): %v", tc.name, err)
		}
		if got != tc.want {
			t.Errorf("RuntimeAvailable(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestRuntimeAvailableInfoError(t *testing.T) {
	client := NewWithAPI(runtimeAPI{err: errors.New("daemon failed")})
	if _, err := client.RuntimeAvailable(context.Background(), "runsc"); err == nil {
		t.Fatal("RuntimeAvailable succeeded despite daemon info error")
	}
}

func TestSecurity(t *testing.T) {
	client := NewWithAPI(runtimeAPI{result: mobyclient.SystemInfoResult{
		Info: system.Info{
			SecurityOptions: []string{"name=seccomp,profile=builtin", "name=rootless"},
			CgroupDriver:    "systemd",
		},
	}})

	got, err := client.Security(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !got.Rootless || got.CgroupDriver != "systemd" {
		t.Fatalf("Security = %+v, want rootless systemd", got)
	}
}

func TestSecurityRootful(t *testing.T) {
	client := NewWithAPI(runtimeAPI{result: mobyclient.SystemInfoResult{
		Info: system.Info{SecurityOptions: []string{"name=seccomp,profile=builtin"}, CgroupDriver: "cgroupfs"},
	}})

	got, err := client.Security(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Rootless || got.CgroupDriver != "cgroupfs" {
		t.Fatalf("Security = %+v, want rootful cgroupfs", got)
	}
}
