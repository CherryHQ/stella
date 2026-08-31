package modelcatalog

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
)

type fakeSnapshotStore struct {
	record SnapshotRecord
	err    error
}

func (f fakeSnapshotStore) GetModelCatalog(context.Context) (SnapshotRecord, error) {
	return f.record, f.err
}

func TestEmbeddedCatalogHasBroadProviderCoverage(t *testing.T) {
	catalog, err := Embedded()
	if err != nil {
		t.Fatal(err)
	}
	if got := len(catalog.Providers(false)); got < 180 {
		t.Fatalf("providers = %d, want at least 180", got)
	}
	if _, ok := catalog.Model("openai", "gpt-5.6-sol"); !ok {
		t.Fatal("embedded catalog lacks representative model")
	}
}

func TestDatabaseSnapshotWinsAndCorruptSnapshotFallsBack(t *testing.T) {
	embedded, err := Embedded()
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(&Catalog{ProvidersByID: map[string]Provider{"only-db": {ID: "only-db", Name: "Database"}}})
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := Load(context.Background(), fakeSnapshotStore{record: SnapshotRecord{Payload: payload}}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got.Lookup("only-db"); !ok {
		t.Fatal("database snapshot was not preferred")
	}

	got, _, err = Load(context.Background(), fakeSnapshotStore{record: SnapshotRecord{Payload: []byte("broken")}}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Providers(false)) != len(embedded.Providers(false)) {
		t.Fatal("corrupt database snapshot did not fall back to embedded")
	}
}

func TestEveryProviderOverrideExistsInEmbeddedCatalog(t *testing.T) {
	catalog, err := Embedded()
	if err != nil {
		t.Fatal(err)
	}
	for id := range providerOverrides {
		if _, ok := catalog.Lookup(id); !ok {
			t.Errorf("override provider %q is absent from embedded catalog", id)
		}
	}
}
