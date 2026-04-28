package agent

import (
	"strings"
	"testing"

	dockerplugin "github.com/vaayne/anna/plugins/sandbox/docker"
)

func withDooDEnv(t *testing.T, inContainer bool, annaHomeHost string) {
	t.Helper()
	prevContainer := runningInContainer
	prevHost := lookupAnnaHomeHost
	runningInContainer = func() bool { return inContainer }
	lookupAnnaHomeHost = func() string { return annaHomeHost }
	t.Cleanup(func() {
		runningInContainer = prevContainer
		lookupAnnaHomeHost = prevHost
	})
}

func TestApplyDooDDefaults_ExplicitPrefixWins(t *testing.T) {
	withDooDEnv(t, true, "/host/anna")
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
	withDooDEnv(t, true, "/Users/v/.anna-dev")
	out, err := applyDooDDefaults(dockerplugin.Config{}, "/workspace")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.ContainerPathPrefix != "/workspace" {
		t.Errorf("ContainerPathPrefix: got %q, want /workspace", out.ContainerPathPrefix)
	}
	if out.HostPathPrefix != "/Users/v/.anna-dev" {
		t.Errorf("HostPathPrefix: got %q, want /Users/v/.anna-dev", out.HostPathPrefix)
	}
}

func TestApplyDooDDefaults_InContainerWithoutEnvErrors(t *testing.T) {
	withDooDEnv(t, true, "")
	_, err := applyDooDDefaults(dockerplugin.Config{}, "/workspace")
	if err == nil {
		t.Fatal("expected error when in-container and ANNA_HOME_HOST unset")
	}
	if !strings.Contains(err.Error(), "ANNA_HOME_HOST") {
		t.Errorf("error should mention ANNA_HOME_HOST, got: %v", err)
	}
	if !strings.Contains(err.Error(), "/workspace") {
		t.Errorf("error should mention annaHome, got: %v", err)
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
