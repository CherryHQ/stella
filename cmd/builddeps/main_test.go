package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/vaayne/anna/internal/builddeps"
)

func TestAppRequiresAtLeastOneSyncMode(t *testing.T) {
	app := newApp(builddeps.Syncer{})
	stderr := new(bytes.Buffer)
	app.Writer = bytes.NewBuffer(nil)
	app.ErrWriter = stderr
	if err := app.Run([]string{"builddeps"}); err == nil {
		t.Fatal("expected error")
	}
}

func TestAppPassesFlagsToSyncer(t *testing.T) {
	var got builddeps.Config
	app := newApp(builddeps.Syncer{
		SyncTools: func(_ context.Context, cfg builddeps.Config) error {
			got = cfg
			return nil
		},
	})
	app.Writer = bytes.NewBuffer(nil)
	app.ErrWriter = bytes.NewBuffer(nil)
	if err := app.Run([]string{"builddeps", "sync", "--tools", "--goos", "linux", "--goarch", "arm64", "--workdir", "/tmp/repo"}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !got.SyncTools {
		t.Fatal("expected SyncTools true")
	}
	if got.GOOS != "linux" || got.GOARCH != "arm64" {
		t.Fatalf("platform = %s/%s, want linux/arm64", got.GOOS, got.GOARCH)
	}
	if got.WorkDir != "/tmp/repo" {
		t.Fatalf("WorkDir = %q, want /tmp/repo", got.WorkDir)
	}
}
