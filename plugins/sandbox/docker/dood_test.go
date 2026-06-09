package docker

import (
	"strings"
	"testing"
)

func withVolumeEnv(t *testing.T, inContainer bool, stellaHomeVolume string) {
	t.Helper()
	prevContainer := runningInContainer
	prevVolume := lookupStellaHomeVolume
	runningInContainer = func() bool { return inContainer }
	lookupStellaHomeVolume = func() string { return stellaHomeVolume }
	t.Cleanup(func() {
		runningInContainer = prevContainer
		lookupStellaHomeVolume = prevVolume
	})
}

func TestApplyVolumeDefaults_ExplicitVolumeWins(t *testing.T) {
	withVolumeEnv(t, true, "env-volume")
	in := Config{StellaHomeVolume: "explicit-volume"}
	out, err := applyVolumeDefaults(in, "/home/nonroot/.stella")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.StellaHomeVolume != "explicit-volume" {
		t.Errorf("StellaHomeVolume: got %q, want explicit-volume", out.StellaHomeVolume)
	}
}

func TestApplyVolumeDefaults_InContainerVolumeSet(t *testing.T) {
	withVolumeEnv(t, true, "stella-data")
	out, err := applyVolumeDefaults(Config{}, "/home/nonroot/.stella")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.StellaHomeVolume != "stella-data" {
		t.Errorf("StellaHomeVolume: got %q, want stella-data", out.StellaHomeVolume)
	}
}

func TestApplyVolumeDefaults_InContainerVolumeUnset_Errors(t *testing.T) {
	withVolumeEnv(t, true, "")
	_, err := applyVolumeDefaults(Config{}, "/home/nonroot/.stella")
	if err == nil {
		t.Fatal("expected error when in-container and STELLA_HOME_VOLUME unset")
	}
	if !strings.Contains(err.Error(), "STELLA_HOME_VOLUME") {
		t.Errorf("error should mention STELLA_HOME_VOLUME, got: %v", err)
	}
	if !strings.Contains(err.Error(), "/home/nonroot/.stella") {
		t.Errorf("error should mention stellaHome, got: %v", err)
	}
}

func TestApplyVolumeDefaults_NotInContainerVolumeSetIgnored(t *testing.T) {
	withVolumeEnv(t, false, "stella-data")
	out, err := applyVolumeDefaults(Config{}, "/home/nonroot/.stella")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.StellaHomeVolume != "" {
		t.Errorf("StellaHomeVolume should be cleared when not in container, got %q", out.StellaHomeVolume)
	}
}

func TestApplyVolumeDefaults_NotInContainerNoVolume(t *testing.T) {
	withVolumeEnv(t, false, "")
	out, err := applyVolumeDefaults(Config{}, "/home/nonroot/.stella")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.StellaHomeVolume != "" {
		t.Errorf("StellaHomeVolume should be empty on native host, got %q", out.StellaHomeVolume)
	}
}
