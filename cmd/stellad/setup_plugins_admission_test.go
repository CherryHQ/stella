package main

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"

	"github.com/CherryHQ/stella/internal/manifestplugins"
)

type closedManifestStorage struct{ err error }

func (s closedManifestStorage) Check(context.Context) error { return s.err }

func TestBackgroundManifestReconcileRequiresStorageAdmission(t *testing.T) {
	home := t.TempDir()
	var tasks sync.WaitGroup
	reconcileManifestPluginsInBackground(t.Context(), &tasks, &manifestplugins.Manifest{}, home, closedManifestStorage{err: errors.New("storage closed")})
	tasks.Wait()
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("closed background reconcile mutated Home: %v", entries)
	}
}
