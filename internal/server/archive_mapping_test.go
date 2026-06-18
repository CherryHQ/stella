package server

import (
	"database/sql"
	"testing"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// CR-021: the API schema declares archived_at on Goal/Task, so the mappers must
// serialize it — otherwise clients can never show when a row was archived.
func TestGoalToAPI_SerializesArchivedAt(t *testing.T) {
	g := sqlc.AgentGoal{
		ID: "g1", UserID: "u1", AgentID: "a1", Title: "g",
		Status: "done", Priority: "routine", ReviewPolicy: "none",
		Context: "{}", Output: "{}",
		CreatedAt: "2026-06-18T00:00:00Z", UpdatedAt: "2026-06-18T00:00:00Z",
		ArchivedAt: sql.NullString{String: "2026-06-18T01:02:03Z", Valid: true},
	}
	if goalToAPI(g).ArchivedAt == nil {
		t.Fatal("goalToAPI dropped archived_at")
	}
	g.ArchivedAt = sql.NullString{}
	if goalToAPI(g).ArchivedAt != nil {
		t.Fatal("goalToAPI invented archived_at for a non-archived goal")
	}
}

func TestTaskToAPI_SerializesArchivedAt(t *testing.T) {
	tk := sqlc.AgentTask{
		ID: "t1", UserID: "u1", AgentID: "a1", Title: "t",
		Status: "done", Priority: "routine", ReviewPolicy: "none",
		Context: "{}", Output: "{}",
		CreatedAt: "2026-06-18T00:00:00Z", UpdatedAt: "2026-06-18T00:00:00Z",
		ArchivedAt: sql.NullString{String: "2026-06-18T01:02:03Z", Valid: true},
	}
	if taskToAPI(tk).ArchivedAt == nil {
		t.Fatal("taskToAPI dropped archived_at")
	}
	tk.ArchivedAt = sql.NullString{}
	if taskToAPI(tk).ArchivedAt != nil {
		t.Fatal("taskToAPI invented archived_at for a non-archived task")
	}
}
