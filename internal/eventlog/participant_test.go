package eventlog_test

import (
	"context"
	"testing"

	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestGroupParticipantNameResolvesHumanAgentSystem(t *testing.T) {
	db := dbtest.New(t)
	q := sqlc.New(db)
	ctx := context.Background()

	if _, err := db.Exec(ctx, `INSERT INTO agent (id, name, model, system_prompt, workspace, sandbox, scope, enabled) VALUES ('agent-anna', 'Anna', 'm', '', '', '{}', 'system', true)`); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	store := eventlog.NewStore(db)
	res, err := store.AppendGroupMessage(ctx, eventlog.Message{
		Platform: "telegram", PlatformGroupID: "g-name", ActorType: eventlog.ActorHuman,
		ActorID: "tg-42", PlatformMessageID: "m-name", Content: `[{"text":"hi"}]`,
	})
	if err != nil {
		t.Fatalf("append: %v", err)
	}

	namer := eventlog.NewParticipantNamer(q)
	if got := namer.Name(ctx, res.GroupID, string(eventlog.ActorAgent), "agent-anna"); got != "Anna" {
		t.Fatalf("agent name = %q, want Anna", got)
	}
	if got := namer.Handle(ctx, res.GroupID, string(eventlog.ActorAgent), "agent-anna"); got != "@Anna" {
		t.Fatalf("agent handle = %q, want @Anna", got)
	}
	if got := namer.Name(ctx, res.GroupID, string(eventlog.ActorSystem), "nudge"); got != "system" {
		t.Fatalf("system name = %q, want system", got)
	}
	// An unknown participant still renders stably: the id is a bad name, not a
	// missing one.
	if got := namer.Name(ctx, res.GroupID, string(eventlog.ActorAgent), "agent-ghost"); got != "agent-ghost" {
		t.Fatalf("unknown agent name = %q, want the raw id", got)
	}
	if got := namer.Line(ctx, res.GroupID, 7, string(eventlog.ActorAgent), "agent-anna", "done"); got != "[seq:7 @Anna]: done" {
		t.Fatalf("line = %q", got)
	}
}

func TestGroupParticipantNameUsesChannelIdentityForPlatformHuman(t *testing.T) {
	db := dbtest.New(t)
	q := sqlc.New(db)
	ctx := context.Background()

	store := eventlog.NewStore(db)
	res, err := store.AppendGroupMessage(ctx, eventlog.Message{
		Platform: "telegram", PlatformGroupID: "g-human", ActorType: eventlog.ActorHuman,
		ActorID: "tg-7", PlatformMessageID: "m-human", Content: `[{"text":"hi"}]`,
	})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	var userID string
	if err := db.QueryRow(ctx, `INSERT INTO auth_user (email, name, role) VALUES ('p@example.com', 'Vee', 'user') RETURNING id`).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO channel_identity (user_id, platform, external_id, name) VALUES ($1, 'telegram', 'tg-7', 'Vee')`, userID); err != nil {
		t.Fatalf("seed identity: %v", err)
	}
	namer := eventlog.NewParticipantNamer(q)
	if got := namer.Line(ctx, res.GroupID, 1, string(eventlog.ActorHuman), "tg-7", "hi"); got != "[seq:1 Vee]: hi" {
		t.Fatalf("human line = %q, want [seq:1 Vee]: hi", got)
	}
}
