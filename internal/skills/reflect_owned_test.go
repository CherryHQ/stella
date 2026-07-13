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
	create(t, Skill{
		Scope:       "user_agent",
		UserID:      userID,
		AgentID:     agentID,
		Name:        "reflect-deprecated",
		Description: "created by reflect but deprecated",
		Status:      "deprecated",
		Metadata:    reflectMetadata,
	})
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
