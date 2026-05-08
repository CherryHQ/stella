package agent

import (
	"strings"
	"testing"

	dockerplugin "github.com/CherryHQ/stella/plugins/sandbox/docker"
)

func withDooDEnv(t *testing.T, inContainer bool, stellaHomeHost string) {
	t.Helper()
	prevContainer := runningInContainer
	prevHost := lookupStellaHomeHost
	runningInContainer = func() bool { return inContainer }
	lookupStellaHomeHost = func() string { return stellaHomeHost }
	t.Cleanup(func() {
		runningInContainer = prevContainer
		lookupStellaHomeHost = prevHost
	})
}

func TestApplyDooDDefaults_ExplicitPrefixWins(t *testing.T) {
	withDooDEnv(t, true, "/host/stella")
	in := dockerplugin.Config{
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
	out, err := applyDooDDefaults(dockerplugin.Config{}, "/workspace")
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
	_, err := applyDooDDefaults(dockerplugin.Config{}, "/workspace")
	if err == nil {
		t.Fatal("expected error when in-container and STELLA_HOME_HOST unset")
	}
	if !strings.Contains(err.Error(), "STELLA_HOME_HOST") {
		t.Errorf("error should mention STELLA_HOME_HOST, got: %v", err)
	}
	if !strings.Contains(err.Error(), "/workspace") {
		t.Errorf("error should mention stellaHome, got: %v", err)
	}
}

func TestApplyDooDDefaults_NotInContainerEnvSetIgnored(t *testing.T) {
	withDooDEnv(t, false, "/some/host/path")
	out, err := applyDooDDefaults(dockerplugin.Config{}, "/workspace")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.ContainerPathPrefix != "" || out.HostPathPrefix != "" {
		t.Errorf("prefixes should stay empty when not in a container, got %+v", out)
	}
}

func TestApplyDooDDefaults_NotInContainerNoEnv(t *testing.T) {
	withDooDEnv(t, false, "")
	out, err := applyDooDDefaults(dockerplugin.Config{}, "/workspace")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.ContainerPathPrefix != "" || out.HostPathPrefix != "" {
		t.Errorf("prefixes should stay empty on host with no env, got %+v", out)
	}
}
