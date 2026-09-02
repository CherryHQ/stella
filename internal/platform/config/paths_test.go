package config

import (
	"strings"
	"testing"
)

func TestStellaHome(t *testing.T) {
	t.Setenv("STELLA_HOME", "")
	ResetStellaHome()
	dir := StellaHome()
	if !strings.HasSuffix(dir, ".stella") {
		t.Errorf("StellaHome() = %q, want suffix .stella", dir)
	}
}

func TestStellaHomeEnv(t *testing.T) {
	t.Setenv("STELLA_HOME", "/custom/stella")
	ResetStellaHome()
	dir := StellaHome()
	if dir != "/custom/stella" {
		t.Errorf("StellaHome() = %q, want %q", dir, "/custom/stella")
	}
}

func TestCachePath(t *testing.T) {
	t.Setenv("STELLA_HOME", "/test/stella")
	ResetStellaHome()
	p := CachePath()
	if p != "/test/stella/cache" {
		t.Errorf("CachePath() = %q, want %q", p, "/test/stella/cache")
	}
}
