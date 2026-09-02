package agent

import (
	"context"
	"testing"

	"github.com/CherryHQ/stella/internal/platform/config"
)

// stubStore satisfies config.Store via an embedded nil interface; only the field
// identity is exercised here, never a method call.
type stubStore struct{ config.Store }

// recordingLoader is a config.SnapshotLoader distinct from the store.
type recordingLoader struct{}

func (recordingLoader) Snapshot(context.Context, string) (*config.Snapshot, error) {
	return &config.Snapshot{}, nil
}

func TestPoolManagerSnapshotLoaderWiring(t *testing.T) {
	st := stubStore{}

	// By default the snapshot loader is the store itself (undecorated behavior).
	pm := NewPoolManager(st, nil)
	if pm.snapshots != config.SnapshotLoader(st) {
		t.Fatalf("default snapshots loader = %#v, want the store", pm.snapshots)
	}

	// WithSnapshotLoader routes Snapshot reads through the decorated loader while
	// leaving the base store (GetAgent, etc.) untouched.
	loader := recordingLoader{}
	pm2 := NewPoolManager(st, nil, WithSnapshotLoader(loader))
	if pm2.snapshots != config.SnapshotLoader(loader) {
		t.Fatal("WithSnapshotLoader did not wire the decorated loader")
	}
	if pm2.store != config.Store(st) {
		t.Fatal("base store must remain the undecorated store")
	}

	// A nil loader is ignored so the store fallback stands.
	pm3 := NewPoolManager(st, nil, WithSnapshotLoader(nil))
	if pm3.snapshots != config.SnapshotLoader(st) {
		t.Fatal("nil loader should leave the store fallback in place")
	}
}
