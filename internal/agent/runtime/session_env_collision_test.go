package runtime_test

import (
	"context"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/plugin"
)

func TestSessionPluginViewRejectsCrossPackageEnvironmentCollision(t *testing.T) {
	db := dbtest.New(t)
	catalog := plugin.NewCatalog()
	for _, id := range []string{"first", "second"} {
		def := plugin.Definition{
			ID: id, DisplayName: id, Source: plugin.SourceBuiltin, DefaultEnabled: true, Revision: 1,
			Spec: []byte(`{"oauth":[{"provider":"` + id + `","bindings":[{"credential":"access_token","env_var":"SHARED_TOKEN"}]}]}`),
		}
		if err := catalog.Register(def); err != nil {
			t.Fatal(err)
		}
	}
	svc := plugin.NewService(db, nil, catalog, plugin.BackendPolicy{}, func(_ context.Context, mutate func() error) error { return mutate() })
	if err := svc.SyncBuiltinDefaults(t.Context()); err != nil {
		t.Fatal(err)
	}
	authority, err := authz.NewSystemAuthority("env-test")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := svc.ResolveSnapshot(t.Context(), authority, "")
	if err != nil {
		t.Fatal(err)
	}
	view, err := sessionPluginView(snapshot)
	if err == nil || !strings.Contains(err.Error(), "SHARED_TOKEN") || !strings.Contains(err.Error(), "first") || !strings.Contains(err.Error(), "second") {
		t.Fatalf("colliding view = %+v, error = %v", view, err)
	}
	if len(view.SessionEnvSpecs) != 0 {
		t.Fatal("failed projection exposed partial credential bindings")
	}
	if _, err := db.Exec(t.Context(), `UPDATE plugin_config SET enabled = false WHERE plugin_id = 'second'`); err != nil {
		t.Fatal(err)
	}
	snapshot, err = svc.ResolveSnapshot(t.Context(), authority, "")
	if err != nil {
		t.Fatal(err)
	}
	view, err = sessionPluginView(snapshot)
	if err != nil || len(view.SessionEnvSpecs) != 1 || view.SessionEnvSpecs[0].OAuthProviderID != "first" {
		t.Fatalf("nonconflicting view = %+v, error = %v", view, err)
	}
}
