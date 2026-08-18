package docker

import (
	"strings"
	"testing"
)

func withDockerModeEnv(t *testing.T, mode, stellaHomeHost, stellaHomeVolume string) {
	t.Helper()
	t.Setenv(dockerSandboxModeEnv, mode)
	t.Setenv("STELLA_DOCKER_RUNTIME", "")
	t.Setenv("STELLA_HOME_HOST", stellaHomeHost)
	t.Setenv("STELLA_HOME_VOLUME", stellaHomeVolume)
}

func TestResolveDockerConfig_Runtime(t *testing.T) {
	withDockerModeEnv(t, "host", "", "")
	t.Setenv("STELLA_DOCKER_RUNTIME", "runsc")

	out, err := resolveDockerConfig(Config{}, "/Users/v/.stella")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Runtime != "runsc" {
		t.Fatalf("Runtime = %q, want runsc", out.Runtime)
	}

	out, err = resolveDockerConfig(Config{Runtime: "kata"}, "/Users/v/.stella")
	if err != nil {
		t.Fatalf("unexpected explicit runtime error: %v", err)
	}
	if out.Runtime != "kata" {
		t.Fatalf("explicit Runtime = %q, want kata", out.Runtime)
	}
}

func TestResolveDockerConfig_Host(t *testing.T) {
	withDockerModeEnv(t, "host", "", "")
	out, err := resolveDockerConfig(Config{}, "/Users/v/.stella")
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

func TestResolveDockerConfig_HostRejectsModeSpecificEnv(t *testing.T) {
	withDockerModeEnv(t, "host", "/host/stella", "")
	_, err := resolveDockerConfig(Config{}, "/home/nonroot/.stella")
	if err == nil {
		t.Fatal("expected error when host mode sets STELLA_HOME_HOST")
	}
	if !strings.Contains(err.Error(), "must not set") {
		t.Errorf("error should reject extra env, got: %v", err)
	}
}

func TestResolveDockerConfig_ExplicitModeWins(t *testing.T) {
	withDockerModeEnv(t, "volume", "", "")
	out, err := resolveDockerConfig(Config{RuntimeMode: DockerSandboxModeHost}, "/Users/v/.stella")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.RuntimeMode != DockerSandboxModeHost {
		t.Errorf("explicit RuntimeMode should win, got %q", out.RuntimeMode)
	}
}

func TestResolveDockerConfig_BindUsesStellaHomeHost(t *testing.T) {
	withDockerModeEnv(t, "bind", "/Users/v/.stella-dev", "")
	out, err := resolveDockerConfig(Config{}, "/home/nonroot/.stella")
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

func TestResolveDockerConfig_BindPreservesExplicitPrefixes(t *testing.T) {
	withDockerModeEnv(t, "bind", "/env/host", "")
	in := Config{
		ContainerPathPrefix: "/explicit/container",
		HostPathPrefix:      "/explicit/host",
	}
	out, err := resolveDockerConfig(in, "/home/nonroot/.stella")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.ContainerPathPrefix != "/explicit/container" || out.HostPathPrefix != "/explicit/host" {
		t.Errorf("explicit prefixes should be preserved, got %+v", out)
	}
}

func TestResolveDockerConfig_BindRequiresHostPath(t *testing.T) {
	withDockerModeEnv(t, "bind", "", "")
	_, err := resolveDockerConfig(Config{}, "/home/nonroot/.stella")
	if err == nil {
		t.Fatal("expected error when bind mode has no STELLA_HOME_HOST")
	}
	if !strings.Contains(err.Error(), "STELLA_HOME_HOST") {
		t.Errorf("error should mention STELLA_HOME_HOST, got: %v", err)
	}
}

func TestResolveDockerConfig_BindRejectsVolume(t *testing.T) {
	withDockerModeEnv(t, "bind", "/host/stella", "stella-data")
	_, err := resolveDockerConfig(Config{}, "/home/nonroot/.stella")
	if err == nil {
		t.Fatal("expected error when bind mode also sets STELLA_HOME_VOLUME")
	}
	if !strings.Contains(err.Error(), "STELLA_HOME_VOLUME") {
		t.Errorf("error should mention STELLA_HOME_VOLUME, got: %v", err)
	}
}

func TestResolveDockerConfig_VolumeUsesStellaHomeVolume(t *testing.T) {
	withDockerModeEnv(t, "volume", "", "stella-data")
	out, err := resolveDockerConfig(Config{}, "/home/nonroot/.stella")
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

func TestResolveDockerConfig_VolumePreservesExplicitVolume(t *testing.T) {
	withDockerModeEnv(t, "volume", "", "env-volume")
	out, err := resolveDockerConfig(Config{StellaHomeVolume: "explicit-volume"}, "/home/nonroot/.stella")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.StellaHomeVolume != "explicit-volume" {
		t.Errorf("StellaHomeVolume: got %q, want explicit-volume", out.StellaHomeVolume)
	}
}

func TestResolveDockerConfig_VolumeRequiresVolume(t *testing.T) {
	withDockerModeEnv(t, "volume", "", "")
	_, err := resolveDockerConfig(Config{}, "/home/nonroot/.stella")
	if err == nil {
		t.Fatal("expected error when volume mode has no STELLA_HOME_VOLUME")
	}
	if !strings.Contains(err.Error(), "STELLA_HOME_VOLUME") {
		t.Errorf("error should mention STELLA_HOME_VOLUME, got: %v", err)
	}
}

func TestResolveDockerConfig_VolumeRejectsHostPath(t *testing.T) {
	withDockerModeEnv(t, "volume", "/host/stella", "stella-data")
	_, err := resolveDockerConfig(Config{}, "/home/nonroot/.stella")
	if err == nil {
		t.Fatal("expected error when volume mode also sets STELLA_HOME_HOST")
	}
	if !strings.Contains(err.Error(), "STELLA_HOME_HOST") {
		t.Errorf("error should mention STELLA_HOME_HOST, got: %v", err)
	}
}

func TestResolveDockerConfig_MissingModeErrors(t *testing.T) {
	withDockerModeEnv(t, "", "", "")
	_, err := resolveDockerConfig(Config{}, "/home/nonroot/.stella")
	if err == nil {
		t.Fatal("expected error when STELLA_DOCKER_SANDBOX_MODE is unset")
	}
	if !strings.Contains(err.Error(), dockerSandboxModeEnv) {
		t.Errorf("error should mention %s, got: %v", dockerSandboxModeEnv, err)
	}
}

func TestResolveDockerConfig_InvalidModeErrors(t *testing.T) {
	withDockerModeEnv(t, "container", "", "")
	_, err := resolveDockerConfig(Config{}, "/home/nonroot/.stella")
	if err == nil {
		t.Fatal("expected error for invalid mode")
	}
	if !strings.Contains(err.Error(), "invalid") {
		t.Errorf("error should mention invalid mode, got: %v", err)
	}
}
