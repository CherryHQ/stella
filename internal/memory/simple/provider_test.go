package simple_test

import (
	"context"
	"path/filepath"
	"testing"

	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/memory/memorytest"
	"github.com/CherryHQ/stella/internal/memory/simple"
)

func TestConformance(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	db, err := appdb.OpenDB(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = db.Close() }()

	orgID, err := appdb.EnsureDefaultOrg(context.Background(), db)
	if err != nil {
		t.Fatalf("ensure default org: %v", err)
	}

	// Seed test agent required by ctx_agent_memory FK constraint.
	_, err = db.Exec(`INSERT INTO settings_agents (id, name, model, model_strong, model_fast, system_prompt, workspace, scope, creator_id, enabled, org_id)
		VALUES ('test', 'Test Agent', '', '', '', '', '', 'system', 0, 1, ?)`, orgID)
	if err != nil {
		t.Fatalf("seed agent: %v", err)
	}

	p := simple.New(db)
	defer func() { _ = p.Close() }()

	memorytest.RunConformance(t, p, orgID)
}
