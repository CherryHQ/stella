package tasks

import (
	"context"
	"time"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
	"github.com/CherryHQ/stella/pkg/memory"
)

func taskSession(task sqlc.AgentTask) memory.Session {
	sessionID := "task:" + task.ID
	if task.SessionID.Valid && task.SessionID.String != "" {
		sessionID = task.SessionID.String
	}
	return memory.Session{
		ID:      sessionID,
		UserID:  task.UserID,
		AgentID: task.AgentID.String,
		Channel: "task",
	}
}

func saveTaskSessionInfo(ctx context.Context, mem memory.Provider, task sqlc.AgentTask, session memory.Session) error {
	sm, ok := mem.(memory.SessionManager)
	if !ok {
		return nil
	}
	return sm.SaveInfo(ctx, memory.SessionInfo{
		ID:         session.ID,
		AgentID:    session.AgentID,
		UserID:     session.UserID,
		Channel:    "task",
		Title:      task.Title,
		LastActive: time.Now().UTC(),
	})
}
