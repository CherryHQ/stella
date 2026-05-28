package store_test

import (
	"testing"

	"github.com/CherryHQ/stella/internal/config"
)

// TestOrphanMCPPluginRowIsHarmless verifies that a legacy `tool/mcp` row in
// settings_plugin does not break list/get paths after the MCP plugin has been
// removed from the codebase. Guards the Phase 1 acceptance that pre-existing
// MCP DB rows are silently ignored.
func TestOrphanMCPPluginRowIsHarmless(t *testing.T) {
	s := setupDBStore(t)
	ctx := testCtx()

	orphan := config.Plugin{
		ID: "tool/mcp", Kind: "tool", Name: "mcp", Enabled: true,
		Config: map[string]any{"servers": []any{}}, OrgID: testOrgID,
	}
	if err := s.UpsertPlugin(ctx, orphan); err != nil {
		t.Fatalf("UpsertPlugin orphan tool/mcp: %v", err)
	}

	if _, err := s.ListPlugins(ctx); err != nil {
		t.Fatalf("ListPlugins: %v", err)
	}

	got, err := s.GetPlugin(ctx, "tool/mcp")
	if err != nil {
		t.Fatalf("GetPlugin tool/mcp: %v", err)
	}
	if got.ID != "tool/mcp" {
		t.Fatalf("GetPlugin returned id=%q want tool/mcp", got.ID)
	}
}
