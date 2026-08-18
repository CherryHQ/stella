package docker

import (
	"strings"
	"testing"
)

func withDockerModeEnv(t *testing.T, mode, stellaHomeHost, stellaHomeVolume string) {
	t.Helper()
	prevMode := lookupDockerSandboxMode
	prevRuntime := lookupDockerRuntime
	prevHost := lookupStellaHomeHost
	prevVolume := lookupStellaHomeVolume
	lookupDockerSandboxMode = func() string { return mode }
	lookupDockerRuntime = func() string { return "" }
	lookupStellaHomeHost = func() string { return stellaHomeHost }
	lookupStellaHomeVolume = func() string { return stellaHomeVolume }
	t.Cleanup(func() {
		lookupDockerSandboxMode = prevMode
		lookupDockerRuntime = prevRuntime
		lookupStellaHomeHost = prevHost
		lookupStellaHomeVolume = prevVolume
	})
}

func TestApplyDockerMode_Runtime(t *testing.T) {
	withDockerModeEnv(t, "host", "", "")
	lookupDockerRuntime = func() string { return "runsc" }

	out, err := applyDockerMode(Config{}, "/Users/v/.stella")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Runtime != "runsc" {
		t.Fatalf("Runtime = %q, want runsc", out.Runtime)
	}

	out, err = applyDockerMode(Config{Runtime: "kata"}, "/Users/v/.stella")
	if err != nil {
		t.Fatalf("unexpected explicit runtime error: %v", err)
	}
	if out.Runtime != "kata" {
		t.Fatalf("explicit Runtime = %q, want kata", out.Runtime)
	}
}

func TestApplyDockerMode_Host(t *testing.T) {
	withDockerModeEnv(t, "host", "", "")
	out, err := applyDockerMode(Config{}, "/Users/v/.stella")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.RuntimeMode != DockerSandboxModeHost {
		t.Errorf("RuntimeMode: got %q, want host", out.RuntimeMode)
	}
	if out.ContainerPathPrefix != "" || out.HostPathPrefix != "" || out.StellaHomeVolume != "" {
		t.Errorf("host mode should not set path translation fields, got %+v", out)
	}
}

func TestApplyDockerMode_HostRejectsModeSpecificEnv(t *testing.T) {
	withDockerModeEnv(t, "host", "/host/stella", "")
	_, err := applyDockerMode(Config{}, "/home/nonroot/.stella")
	if err == nil {
		t.Fatal("expected error when host mode sets STELLA_HOME_HOST")
	}
	if !strings.Contains(err.Error(), "must not set") {
		t.Errorf("error should reject extra env, got: %v", err)
	}
}

func TestApplyDockerMode_ExplicitModeWins(t *testing.T) {
	withDockerModeEnv(t, "volume", "", "")
	out, err := applyDockerMode(Config{RuntimeMode: DockerSandboxModeHost}, "/Users/v/.stella")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.RuntimeMode != DockerSandboxModeHost {
		t.Errorf("explicit RuntimeMode should win, got %q", out.RuntimeMode)
	}
}

func TestApplyDockerMode_BindUsesStellaHomeHost(t *testing.T) {
	withDockerModeEnv(t, "bind", "/Users/v/.stella-dev", "")
	out, err := applyDockerMode(Config{}, "/home/nonroot/.stella")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.RuntimeMode != DockerSandboxModeBind {
		t.Errorf("RuntimeMode: got %q, want bind", out.RuntimeMode)
	}
	if out.ContainerPathPrefix != "/home/nonroot/.stella" {
		t.Errorf("ContainerPathPrefix: got %q, want /home/nonroot/.stella", out.ContainerPathPrefix)
	}
	if out.HostPathPrefix != "/Users/v/.stella-dev" {
		t.Errorf("HostPathPrefix: got %q, want /Users/v/.stella-dev", out.HostPathPrefix)
	}
}

func TestApplyDockerMode_BindPreservesExplicitPrefixes(t *testing.T) {
	withDockerModeEnv(t, "bind", "/env/host", "")
	in := Config{
		ContainerPathPrefix: "/explicit/container",
		HostPathPrefix:      "/explicit/host",
	}
	out, err := applyDockerMode(in, "/home/nonroot/.stella")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.ContainerPathPrefix != "/explicit/container" || out.HostPathPrefix != "/explicit/host" {
		t.Errorf("explicit prefixes should be preserved, got %+v", out)
	}
}

func TestApplyDockerMode_BindRequiresHostPath(t *testing.T) {
	withDockerModeEnv(t, "bind", "", "")
	_, err := applyDockerMode(Config{}, "/home/nonroot/.stella")
	if err == nil {
		t.Fatal("expected error when bind mode has no STELLA_HOME_HOST")
	}
	if !strings.Contains(err.Error(), "STELLA_HOME_HOST") {
		t.Errorf("error should mention STELLA_HOME_HOST, got: %v", err)
	}
}

func TestApplyDockerMode_BindRejectsVolume(t *testing.T) {
	withDockerModeEnv(t, "bind", "/host/stella", "stella-data")
	_, err := applyDockerMode(Config{}, "/home/nonroot/.stella")
	if err == nil {
		t.Fatal("expected error when bind mode also sets STELLA_HOME_VOLUME")
	}
	if !strings.Contains(err.Error(), "STELLA_HOME_VOLUME") {
		t.Errorf("error should mention STELLA_HOME_VOLUME, got: %v", err)
	}
}

func TestApplyDockerMode_VolumeUsesStellaHomeVolume(t *testing.T) {
	withDockerModeEnv(t, "volume", "", "stella-data")
	out, err := applyDockerMode(Config{}, "/home/nonroot/.stella")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.RuntimeMode != DockerSandboxModeVolume {
		t.Errorf("RuntimeMode: got %q, want volume", out.RuntimeMode)
	}
	if out.StellaHomeVolume != "stella-data" {
		t.Errorf("StellaHomeVolume: got %q, want stella-data", out.StellaHomeVolume)
	}
}

func TestApplyDockerMode_VolumePreservesExplicitVolume(t *testing.T) {
	withDockerModeEnv(t, "volume", "", "env-volume")
	out, err := applyDockerMode(Config{StellaHomeVolume: "explicit-volume"}, "/home/nonroot/.stella")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.StellaHomeVolume != "explicit-volume" {
		t.Errorf("StellaHomeVolume: got %q, want explicit-volume", out.StellaHomeVolume)
	}
}

func TestApplyDockerMode_VolumeRequiresVolume(t *testing.T) {
	withDockerModeEnv(t, "volume", "", "")
	_, err := applyDockerMode(Config{}, "/home/nonroot/.stella")
	if err == nil {
		t.Fatal("expected error when volume mode has no STELLA_HOME_VOLUME")
	}
	if !strings.Contains(err.Error(), "STELLA_HOME_VOLUME") {
		t.Errorf("error should mention STELLA_HOME_VOLUME, got: %v", err)
	}
}

func TestApplyDockerMode_VolumeRejectsHostPath(t *testing.T) {
	withDockerModeEnv(t, "volume", "/host/stella", "stella-data")
	_, err := applyDockerMode(Config{}, "/home/nonroot/.stella")
	if err == nil {
		t.Fatal("expected error when volume mode also sets STELLA_HOME_HOST")
	}
	if !strings.Contains(err.Error(), "STELLA_HOME_HOST") {
		t.Errorf("error should mention STELLA_HOME_HOST, got: %v", err)
	}
}

func TestApplyDockerMode_MissingModeErrors(t *testing.T) {
	withDockerModeEnv(t, "", "", "")
	_, err := applyDockerMode(Config{}, "/home/nonroot/.stella")
	if err == nil {
		t.Fatal("expected error when STELLA_DOCKER_SANDBOX_MODE is unset")
	}
	if !strings.Contains(err.Error(), dockerSandboxModeEnv) {
		t.Errorf("error should mention %s, got: %v", dockerSandboxModeEnv, err)
	}
}

func TestApplyDockerMode_InvalidModeErrors(t *testing.T) {
	withDockerModeEnv(t, "container", "", "")
	_, err := applyDockerMode(Config{}, "/home/nonroot/.stella")
	if err == nil {
		t.Fatal("expected error for invalid mode")
	}
	if !strings.Contains(err.Error(), "invalid") {
		t.Errorf("error should mention invalid mode, got: %v", err)
	}
}
