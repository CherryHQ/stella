package lcm_test

import (
	"context"
	"testing"

	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/memory/lcm"
	"github.com/CherryHQ/stella/internal/memory/memorytest"
)

func TestConformance(t *testing.T) {
	db := dbtest.New(t)

	// Seed test agent + user required by ctx_agent_memory / ctx_conversation FK constraints.
	_, err := db.Exec(`INSERT INTO agent (id, name, model, model_strong, model_fast, system_prompt, workspace, scope, creator_id, enabled)
		VALUES ('test', 'Test Agent', '', '', '', '', '', 'system', 0, true)`)
	if err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	if _, err = db.Exec(`INSERT INTO auth_user (id, email) VALUES ($1, 'user-1@test.local'), ($2, 'profile@test.local')`,
		memorytest.SessionUserID, memorytest.ProfileUserID); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	p, err := lcm.New(db, func(context.Context, string) (string, error) {
		return "conformance summary", nil
	}, nil)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	defer func() { _ = p.Close() }()

	memorytest.RunConformance(t, p)
}
