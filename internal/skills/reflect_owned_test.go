package skills

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/CherryHQ/stella/internal/config"
	cfgstore "github.com/CherryHQ/stella/internal/store"
)

func TestMarkReflectOwnedMetadataPreservesExistingFields(t *testing.T) {
	metadata, err := MarkReflectOwnedMetadata(json.RawMessage(`{"created-at":"2026-07-01T00:00:00Z","source":"manual"}`))
	if err != nil {
		t.Fatalf("MarkReflectOwnedMetadata: %v", err)
	}

	var got map[string]string
	if err := json.Unmarshal(metadata, &got); err != nil {
		t.Fatalf("unmarshal metadata: %v", err)
	}

	if got["created_by"] != ReflectSkillCreatedBy {
		t.Fatalf("created_by = %q, want %q", got["created_by"], ReflectSkillCreatedBy)
	}
	if got["created-at"] != "2026-07-01T00:00:00Z" {
		t.Fatalf("created-at was not preserved: %#v", got)
	}
	if got["source"] != "manual" {
		t.Fatalf("source was not preserved: %#v", got)
	}
}

func TestMarkReflectOwnedMetadataAcceptsWhitespaceNull(t *testing.T) {
	metadata, err := MarkReflectOwnedMetadata(json.RawMessage(" \nnull\t"))
	if err != nil {
		t.Fatalf("MarkReflectOwnedMetadata: %v", err)
	}
	if !json.Valid(metadata) {
		t.Fatalf("metadata is not valid JSON: %q", metadata)
	}
	if !IsReflectOwned(Skill{Metadata: metadata}) {
		t.Fatalf("metadata is not reflect-owned: %s", metadata)
	}

	var fields map[string]any
	if err := json.Unmarshal(metadata, &fields); err != nil {
		t.Fatalf("unmarshal metadata: %v", err)
	}
	if len(fields) != 1 || fields[reflectSkillCreatedByKey] != ReflectSkillCreatedBy {
		t.Fatalf("metadata fields = %#v, want only reflect ownership", fields)
	}
}

func TestListActiveReflectOwnedUserAgentSkills(t *testing.T) {
	store, db, ctx := newTestStore(t)
	userID, agentID := seedFixtures(t, db)
	otherAgentID := "agent2"
	if err := cfgstore.NewDBStore(db).CreateAgent(ctx, config.Agent{
		ID: otherAgentID, Name: otherAgentID, Model: "p/m", Workspace: "/tmp/" + otherAgentID, Enabled: true,
	}); err != nil {
		t.Fatalf("seed other agent: %v", err)
	}

	reflectMetadata, err := MarkReflectOwnedMetadata(json.RawMessage(`{"created-at":"2026-07-01T00:00:00Z"}`))
	if err != nil {
		t.Fatalf("MarkReflectOwnedMetadata: %v", err)
	}

	create := func(t *testing.T, sk Skill) string {
		t.Helper()
		id, err := store.Create(ctx, sk, map[string]string{MainFile: "# " + sk.Name})
		if err != nil {
			t.Fatalf("create %s: %v", sk.Name, err)
		}
		return id
	}

	// Reflect ownership is only established by the dedicated writer; generic
	// Create deliberately normalizes user-originated metadata to manual.
	reflectOwned, err := store.CreateReflectOwnedUserAgentSkill(ctx, ReflectSkillCreate{
		UserID: userID, AgentID: agentID, Name: "reflect-owned-active",
		Description: "created by reflect", MainFileContent: "# reflect-owned-active", Metadata: reflectMetadata,
	})
	if err != nil {
		t.Fatalf("CreateReflectOwnedUserAgentSkill: %v", err)
	}
	wantID := reflectOwned.ID
	create(t, Skill{
		Scope:       "user_agent",
		UserID:      userID,
		AgentID:     agentID,
		Name:        "manual-active",
		Description: "created manually",
		Status:      "active",
		Metadata:    json.RawMessage(`{"created_by":"manual"}`),
	})
	deprecated, err := store.CreateReflectOwnedUserAgentSkill(ctx, ReflectSkillCreate{
		UserID: userID, AgentID: agentID, Name: "reflect-deprecated",
		Description: "created by reflect but deprecated", MainFileContent: "# reflect-deprecated", Metadata: reflectMetadata,
	})
	if err != nil {
		t.Fatalf("create deprecated fixture: %v", err)
	}
	// Deprecated is a legacy read-only state; seed it below the business API.
	if _, err := db.Exec(ctx, `UPDATE skill SET status = 'deprecated' WHERE id = $1`, deprecated.ID); err != nil {
		t.Fatalf("seed deprecated fixture: %v", err)
	}
	create(t, Skill{
		Scope:       "user",
		UserID:      userID,
		Name:        "reflect-user-scope",
		Description: "reflect owned but not user_agent",
		Status:      "active",
		Metadata:    reflectMetadata,
	})
	create(t, Skill{
		Scope:       "user_agent",
		UserID:      userID,
		AgentID:     otherAgentID,
		Name:        "reflect-other-context",
		Description: "reflect owned in another context",
		Status:      "active",
		Metadata:    reflectMetadata,
	})

	rows, err := store.ListActiveReflectOwnedUserAgentSkills(context.Background(), userID, agentID)
	if err != nil {
		t.Fatalf("ListActiveReflectOwnedUserAgentSkills: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1: %#v", len(rows), rows)
	}
	if rows[0].ID != wantID {
		t.Fatalf("row ID = %q, want %q", rows[0].ID, wantID)
	}
	if !IsReflectOwned(rows[0]) {
		t.Fatalf("returned row is not marked reflect-owned: %#v", rows[0])
	}
}
