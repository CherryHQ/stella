package db

import (
	"errors"
	"testing"

	"github.com/CherryHQ/stella/internal/plugin"
)

func TestFilesystemOAuthImportTargets(t *testing.T) {
	for _, fileFlow := range []bool{true, false} {
		name := "legacy_missing_target"
		if fileFlow {
			name = "file_without_catalog"
		}
		t.Run(name, func(t *testing.T) {
			db := newTestDB(t)
			user := insertPluginUser(t, db, name+"@example.test", false)
			config := `{"client_id":"client"}`
			if fileFlow {
				config = `{"file":true,"client_id":"client"}`
			}
			if _, err := db.Exec(t.Context(), `
				INSERT INTO mcp_oauth_flow(server_id,user_id,credential_scope,pkce_verifier,oauth_config,expires_at)
				VALUES ('00000000-0000-4000-8000-000000000046',$1,'user','verifier',$2,now()+interval '5 minutes')
			`, user.UserID(), config); err != nil {
				t.Fatal(err)
			}
			err := plugin.ImportLegacyState(t.Context(), db, plugin.NewCatalog(), nil, nil)
			if fileFlow && err != nil {
				t.Fatalf("file flow blocked legacy import: %v", err)
			}
			if !fileFlow && !errors.Is(err, plugin.ErrLegacyMigrationConflict) {
				t.Fatalf("missing legacy target was not rejected: %v", err)
			}
			var count int
			if err := db.QueryRow(t.Context(), `SELECT count(*) FROM mcp_oauth_flow`).Scan(&count); err != nil || count != 1 {
				t.Fatalf("flow not preserved: count=%d error=%v", count, err)
			}
		})
	}
}
