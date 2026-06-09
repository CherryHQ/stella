package docker

import (
	"strings"
	"testing"
)

func withDooDEnv(t *testing.T, inContainer bool, stellaHomeHost string) {
	t.Helper()
	withDooDEnvFull(t, inContainer, stellaHomeHost, "")
}

func withDooDEnvFull(t *testing.T, inContainer bool, stellaHomeHost, stellaHomeVolume string) {
	t.Helper()
	prevContainer := runningInContainer
	prevHost := lookupStellaHomeHost
	prevVolume := lookupStellaHomeVolume
	runningInContainer = func() bool { return inContainer }
	lookupStellaHomeHost = func() string { return stellaHomeHost }
	lookupStellaHomeVolume = func() string { return stellaHomeVolume }
	t.Cleanup(func() {
		runningInContainer = prevContainer
		lookupStellaHomeHost = prevHost
		lookupStellaHomeVolume = prevVolume
	})
}

func TestApplyDooDDefaults_ExplicitPrefixWins(t *testing.T) {
	withDooDEnv(t, true, "/host/stella")
	in := Config{
		ContainerPathPrefix: "/explicit/container",
		HostPathPrefix:      "/explicit/host",
	}
	out, err := applyDooDDefaults(in, "/workspace")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.ContainerPathPrefix != "/explicit/container" || out.HostPathPrefix != "/explicit/host" {
		t.Errorf("explicit prefixes should be preserved, got %+v", out)
	}
}

func TestApplyDooDDefaults_InContainerWithEnv(t *testing.T) {
	withDooDEnv(t, true, "/Users/v/.stella-dev")
	out, err := applyDooDDefaults(Config{}, "/workspace")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.ContainerPathPrefix != "/workspace" {
		t.Errorf("ContainerPathPrefix: got %q, want /workspace", out.ContainerPathPrefix)
	}
	if out.HostPathPrefix != "/Users/v/.stella-dev" {
		t.Errorf("HostPathPrefix: got %q, want /Users/v/.stella-dev", out.HostPathPrefix)
	}
}

func TestApplyDooDDefaults_InContainerWithoutEnvErrors(t *testing.T) {
	withDooDEnv(t, true, "")
	_, err := applyDooDDefaults(Config{}, "/workspace")
	if err == nil {
		t.Fatal("expected error when in-container and neither STELLA_HOME_HOST nor STELLA_HOME_VOLUME set")
	}
	if !strings.Contains(err.Error(), "STELLA_HOME_HOST") {
		t.Errorf("error should mention STELLA_HOME_HOST, got: %v", err)
	}
	if !strings.Contains(err.Error(), "STELLA_HOME_VOLUME") {
		t.Errorf("error should mention STELLA_HOME_VOLUME, got: %v", err)
	}
	if !strings.Contains(err.Error(), "/workspace") {
		t.Errorf("error should mention stellaHome, got: %v", err)
	}
}

func TestApplyDooDDefaults_InContainerVolumeMode(t *testing.T) {
	withDooDEnvFull(t, true, "", "stella-data")
	out, err := applyDooDDefaults(Config{}, "/home/nonroot/.stella")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.StellaHomeVolume != "stella-data" {
		t.Errorf("StellaHomeVolume: got %q, want stella-data", out.StellaHomeVolume)
	}
	if out.ContainerPathPrefix != "" || out.HostPathPrefix != "" {
		t.Errorf("bind-mount prefixes should be empty in volume mode, got %+v", out)
	}
}

func TestApplyDooDDefaults_InContainerBothSetHostWins(t *testing.T) {
	withDooDEnvFull(t, true, "/host/stella", "stella-data")
	out, err := applyDooDDefaults(Config{}, "/home/nonroot/.stella")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// STELLA_HOME_HOST wins; volume mode is cleared
	if out.ContainerPathPrefix != "/home/nonroot/.stella" {
		t.Errorf("ContainerPathPrefix: got %q, want /home/nonroot/.stella", out.ContainerPathPrefix)
	}
	if out.HostPathPrefix != "/host/stella" {
		t.Errorf("HostPathPrefix: got %q, want /host/stella", out.HostPathPrefix)
	}
	if out.StellaHomeVolume != "" {
		t.Errorf("StellaHomeVolume should be cleared when STELLA_HOME_HOST wins, got %q", out.StellaHomeVolume)
	}
}

func TestApplyDooDDefaults_NotInContainerVolumeIgnored(t *testing.T) {
	withDooDEnvFull(t, false, "", "stella-data")
	out, err := applyDooDDefaults(Config{}, "/home/nonroot/.stella")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.StellaHomeVolume != "" {
		t.Errorf("StellaHomeVolume should be cleared when not in container, got %q", out.StellaHomeVolume)
	}
}

func TestApplyDooDDefaults_NotInContainerEnvSetIgnored(t *testing.T) {
	withDooDEnv(t, false, "/some/host/path")
	out, err := applyDooDDefaults(Config{}, "/workspace")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.ContainerPathPrefix != "" || out.HostPathPrefix != "" {
		t.Errorf("prefixes should stay empty when not in a container, got %+v", out)
	}
}

func TestApplyDooDDefaults_NotInContainerNoEnv(t *testing.T) {
	withDooDEnv(t, false, "")
	out, err := applyDooDDefaults(Config{}, "/workspace")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.ContainerPathPrefix != "" || out.HostPathPrefix != "" {
		t.Errorf("prefixes should stay empty on host with no env, got %+v", out)
	}
}
