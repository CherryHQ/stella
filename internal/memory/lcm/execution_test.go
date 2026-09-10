package lcm_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/lcm"
	"github.com/CherryHQ/stella/internal/sessionexecution"
	"github.com/CherryHQ/stella/pkg/ai"
)

func TestExecutionFencesTranscriptMemorySnapshotAndActivity(t *testing.T) {
	db := dbtest.New(t)
	userID := uuid.Must(uuid.NewV7()).String()
	if _, err := db.Exec(t.Context(), "INSERT INTO auth_user(id,email) VALUES($1,'execution@test.invalid')", userID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(t.Context(), "INSERT INTO agent(id,name,model,workspace) VALUES('agent','Agent','test/model','/tmp/test')"); err != nil {
		t.Fatal(err)
	}
	p, err := lcm.New(db, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	sess := memory.Session{ID: uuid.Must(uuid.NewV7()).String(), UserID: userID, AgentID: "agent"}
	if err := p.Bootstrap(t.Context(), sess); err != nil {
		t.Fatal(err)
	}
	store := sessionexecution.New(db)
	ctx, lease, err := store.Claim(t.Context(), sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lease.Finish("error") }()
	if err := p.Append(ctx, sess, ai.UserMessage{Content: "accepted"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(t.Context(), "UPDATE ctx_session_execution SET lease_until=clock_timestamp()-interval '1 second' WHERE session_id=$1", sess.ID); err != nil {
		t.Fatal(err)
	}
	_, next, err := store.Claim(t.Context(), sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = next.Finish("error") }()
	stale := context.WithoutCancel(ctx)
	checks := []struct {
		name  string
		write func() error
	}{
		{"transcript", func() error { return p.Append(stale, sess, ai.UserMessage{Content: "stale"}) }},
		{"profile", func() error { return p.SetProfile(stale, userID, "agent", "stale profile") }},
		{"snapshot", func() error { _, err := p.GetOrCreateSessionSnapshot(stale, sess.ID, userID, "agent"); return err }},
		{"activity", func() error { _, err := p.MarkSessionTurnCompleted(stale, sess, memory.SessionTurnSuccess); return err }},
	}
	for _, check := range checks {
		if err := check.write(); !errors.Is(err, sessionexecution.ErrLost) {
			t.Fatalf("%s: %v", check.name, err)
		}
	}
	if err := p.SetProfile(t.Context(), userID, "agent", "user change"); err != nil {
		t.Fatalf("independent user write: %v", err)
	}
	if err := next.Finish("success"); err != nil {
		t.Fatal(err)
	}
}
