package controlplane

import (
	"context"
	"errors"
	"testing"
)

type closedStorage struct{ err error }

func (s closedStorage) Check(context.Context) error { return s.err }

func TestHomeBackedControlPlaneOperationsRequireStorageAdmission(t *testing.T) {
	gateErr := errors.New("storage closed")
	access, err := NewService(nil, nil, nil, nil, nil, WithStorageAdmission(closedStorage{err: gateErr})).Begin(t.Context(), adminAuthority(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := access.SearchCliToolRegistry(t.Context(), "tool", 1); !errors.Is(err, gateErr) {
		t.Fatalf("SearchCliToolRegistry error = %v", err)
	}
	if _, err := access.CliToolLatest(t.Context(), "tool"); !errors.Is(err, gateErr) {
		t.Fatalf("CliToolLatest error = %v", err)
	}
	if _, err := access.SyncManifestPlugins(t.Context()); !errors.Is(err, gateErr) {
		t.Fatalf("SyncManifestPlugins error = %v", err)
	}
}
