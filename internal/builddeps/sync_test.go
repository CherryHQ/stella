package builddeps

import (
	"context"
	"runtime"
	"testing"
)

func TestConfigNormalizedDefaultsRuntimePlatform(t *testing.T) {
	cfg := (Config{SyncTools: true}).Normalized()
	if cfg.GOOS != runtime.GOOS {
		t.Fatalf("GOOS = %q, want %q", cfg.GOOS, runtime.GOOS)
	}
	if cfg.GOARCH != runtime.GOARCH {
		t.Fatalf("GOARCH = %q, want %q", cfg.GOARCH, runtime.GOARCH)
	}
	if cfg.WorkDir != "." {
		t.Fatalf("WorkDir = %q, want .", cfg.WorkDir)
	}
}

func TestConfigValidateRequiresAtLeastOneMode(t *testing.T) {
	if err := (Config{}).Normalized().Validate(); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestSyncerRunCallsOnlySelectedModes(t *testing.T) {
	var skillsCalled, toolsCalled bool
	s := Syncer{
		SyncSkills: func(_ context.Context, cfg Config) error {
			skillsCalled = true
			if cfg.WorkDir == "" {
				t.Fatal("expected normalized workdir")
			}
			return nil
		},
		SyncTools: func(_ context.Context, _ Config) error {
			toolsCalled = true
			return nil
		},
	}
	if err := s.Run(context.Background(), Config{SyncSkills: true}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !skillsCalled {
		t.Fatal("expected skill sync to run")
	}
	if toolsCalled {
		t.Fatal("did not expect tool sync to run")
	}
}

func TestSyncerRunFailsClosedForMissingHandler(t *testing.T) {
	err := (Syncer{}).Run(context.Background(), Config{SyncTools: true})
	if err == nil || err.Error() != "tool sync not implemented" {
		t.Fatalf("Run() error = %v, want tool sync not implemented", err)
	}
}
