package db

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

const (
	groupFactBeforeMigration = 90000000000015
	groupFactMigration       = 90000000000016
)

func TestGroupFactMigrationCreatesAtomicFactSchema(t *testing.T) {
	db := newTestDB(t)
	provider, closeProvider := groupLCMMigrationProvider(t, db)
	defer closeProvider()
	ctx := context.Background()

	if _, err := provider.DownTo(ctx, groupFactBeforeMigration); err != nil {
		t.Fatalf("restore pre-Group-Fact schema: %v", err)
	}
	if tableExists(t, db, "ctx_group_fact") || tableExists(t, db, "ctx_group_fact_changelog") {
		t.Fatal("Group Fact tables exist before their migration")
	}
	if _, err := provider.UpTo(ctx, groupFactMigration); err != nil {
		t.Fatalf("apply Group Fact migration: %v", err)
	}
	if !tableExists(t, db, "ctx_group_fact") || !tableExists(t, db, "ctx_group_fact_changelog") {
		t.Fatal("Group Fact tables were not created")
	}
}

func TestGroupFactMigrationEnforcesSubjectShapeAndSource(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	groupID := uuid.NewString()
	if _, err := db.Exec(ctx, `
		INSERT INTO ctx_group_state (id, platform, platform_group_id)
		VALUES ($1, 'test', 'group-fact-constraints')
	`, groupID); err != nil {
		t.Fatalf("seed group: %v", err)
	}

	insert := func(subject string, subjectID any, content, status, source string) error {
		_, err := db.Exec(ctx, `
			INSERT INTO ctx_group_fact (
				id, group_id, subject, subject_id, content, status, source
			)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
		`, uuid.NewString(), groupID, subject, subjectID, content, status, source)
		return err
	}

	if err := insert("group", nil, "Release changes require review.", "active", "reflect"); err != nil {
		t.Fatalf("insert valid group Fact: %v", err)
	}
	if err := insert("human", "alice", "Coordinates customer testing.", "active", "reflect"); err != nil {
		t.Fatalf("insert valid human Fact: %v", err)
	}
	for name, err := range map[string]error{
		"group subject id":       insert("group", "unexpected", "invalid", "active", "reflect"),
		"missing human id":       insert("human", nil, "invalid", "active", "reflect"),
		"empty content":          insert("agent", "agent-1", " ", "active", "reflect"),
		"unsupported status":     insert("agent", "agent-1", "invalid", "pending", "reflect"),
		"unsupported provenance": insert("agent", "agent-1", "invalid", "manual", "manual"),
	} {
		if err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
}
