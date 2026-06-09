package docker

import (
	"strings"
	"testing"
)

func withDooDEnv(t *testing.T, inContainer bool, stellaHomeHost, stellaHomeVolume string) {
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

func TestApplyDooDDefaults_ExplicitBindPrefixWins(t *testing.T) {
	withDooDEnv(t, true, "/host/stella", "")
	in := Config{
		ContainerPathPrefix: "/explicit/container",
		HostPathPrefix:      "/explicit/host",
	}
	out, err := applyDooDDefaults(in, "/home/nonroot/.stella")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.ContainerPathPrefix != "/explicit/container" || out.HostPathPrefix != "/explicit/host" {
		t.Errorf("explicit prefixes should be preserved, got %+v", out)
	}
}

func TestApplyDooDDefaults_InContainerBindMount(t *testing.T) {
	withDooDEnv(t, true, "/Users/v/.stella-dev", "")
	out, err := applyDooDDefaults(Config{}, "/home/nonroot/.stella")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.ContainerPathPrefix != "/home/nonroot/.stella" {
		t.Errorf("ContainerPathPrefix: got %q, want /home/nonroot/.stella", out.ContainerPathPrefix)
	}
	if out.HostPathPrefix != "/Users/v/.stella-dev" {
		t.Errorf("HostPathPrefix: got %q, want /Users/v/.stella-dev", out.HostPathPrefix)
	}
}

func TestApplyDooDDefaults_ExplicitVolumeWins(t *testing.T) {
	withDooDEnv(t, true, "", "env-volume")
	in := Config{StellaHomeVolume: "explicit-volume"}
	out, err := applyDooDDefaults(in, "/home/nonroot/.stella")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.StellaHomeVolume != "explicit-volume" {
		t.Errorf("StellaHomeVolume: got %q, want explicit-volume", out.StellaHomeVolume)
	}
}

func TestApplyDooDDefaults_InContainerVolumeSet(t *testing.T) {
	withDooDEnv(t, true, "", "stella-data")
	out, err := applyDooDDefaults(Config{}, "/home/nonroot/.stella")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.StellaHomeVolume != "stella-data" {
		t.Errorf("StellaHomeVolume: got %q, want stella-data", out.StellaHomeVolume)
	}
}

func TestApplyDooDDefaults_InContainerWithoutModeErrors(t *testing.T) {
	withDooDEnv(t, true, "", "")
	_, err := applyDooDDefaults(Config{}, "/home/nonroot/.stella")
	if err == nil {
		t.Fatal("expected error when in-container and no docker-sandbox mount mode is set")
	}
	if !strings.Contains(err.Error(), "STELLA_HOME_HOST") || !strings.Contains(err.Error(), "STELLA_HOME_VOLUME") {
		t.Errorf("error should mention both docker-sandbox modes, got: %v", err)
	}
	if !strings.Contains(err.Error(), "/home/nonroot/.stella") {
		t.Errorf("error should mention stellaHome, got: %v", err)
	}
}

func TestApplyDooDDefaults_InContainerBothModesError(t *testing.T) {
	withDooDEnv(t, true, "/host/stella", "stella-data")
	_, err := applyDooDDefaults(Config{}, "/home/nonroot/.stella")
	if err == nil {
		t.Fatal("expected error when both STELLA_HOME_HOST and STELLA_HOME_VOLUME are set")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("error should mention mutual exclusion, got: %v", err)
	}
}

func TestApplyDooDDefaults_NotInContainerVolumeSetIgnored(t *testing.T) {
	withDooDEnv(t, false, "", "stella-data")
	out, err := applyDooDDefaults(Config{}, "/home/nonroot/.stella")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.StellaHomeVolume != "" {
		t.Errorf("StellaHomeVolume should be cleared when not in container, got %q", out.StellaHomeVolume)
	}
}

func TestApplyDooDDefaults_NotInContainerBindSetIgnored(t *testing.T) {
	withDooDEnv(t, false, "/host/stella", "")
	out, err := applyDooDDefaults(Config{}, "/home/nonroot/.stella")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.ContainerPathPrefix != "" || out.HostPathPrefix != "" {
		t.Errorf("prefixes should be cleared when not in container, got %+v", out)
	}
}

func TestApplyDooDDefaults_NotInContainerBothModesIgnored(t *testing.T) {
	withDooDEnv(t, false, "/host/stella", "stella-data")
	out, err := applyDooDDefaults(Config{}, "/home/nonroot/.stella")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.StellaHomeVolume != "" || out.ContainerPathPrefix != "" || out.HostPathPrefix != "" {
		t.Errorf("docker-sandbox mode fields should be cleared on native host, got %+v", out)
	}
}

func TestApplyDooDDefaults_NotInContainerNoMode(t *testing.T) {
	withDooDEnv(t, false, "", "")
	out, err := applyDooDDefaults(Config{}, "/home/nonroot/.stella")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.StellaHomeVolume != "" || out.ContainerPathPrefix != "" || out.HostPathPrefix != "" {
		t.Errorf("docker-sandbox mode fields should be empty on native host, got %+v", out)
	}
}
