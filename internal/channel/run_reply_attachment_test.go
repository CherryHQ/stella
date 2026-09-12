package channel

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	cfgstore "github.com/CherryHQ/stella/cmd/stellad/store"
	agentrun "github.com/CherryHQ/stella/internal/agent/run"
	choutbox "github.com/CherryHQ/stella/internal/channel/outbox"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// A run reply's file bytes are staged at prepare time: prepareReply reads the
// workspace file into the op's attachment, the finish transaction appends it,
// and any replica's send boundary re-reads the committed bytea — never the
// path. Mutating and deleting the source after commit must not change what
// the recovering sender delivers.
func TestRunReplyAttachmentSurvivesSourceChange(t *testing.T) {
	db := dbtest.New(t)
	ctx := context.Background()
	q := sqlc.New(db)
	if _, err := q.CreateAgent(ctx, sqlc.CreateAgentParams{
		ID: "agent-att", Name: "agent-att", Workspace: t.TempDir(),
		Sandbox: []byte(`{}`), Scope: "system", Enabled: true,
	}); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	// telegram plans attachment ops; the channel row only steers the reply
	// plan — channel_outbox stores no channel FK.
	if _, err := q.CreateChannel(ctx, sqlc.CreateChannelParams{
		ID: "ch-att", Name: "ch-att", Type: "telegram",
		AgentID: pgtype.Text{String: "agent-att", Valid: true}, Enabled: true,
		Config: `{}`,
	}); err != nil {
		t.Fatalf("create channel: %v", err)
	}
	sessionID := "sess-att"
	if _, err := q.CreateConversation(ctx, sqlc.CreateConversationParams{
		ID: "60000000-0000-0000-0000-000000000001", SessionID: sessionID,
		Kind: "chat", Channel: "telegram",
		AgentID:    pgtype.Text{String: "agent-att", Valid: true},
		UserID:     pgtype.Text{String: "user-att", Valid: true},
		LastActive: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	r, err := q.CreateAgentRun(ctx, sqlc.CreateAgentRunParams{
		SessionID: sessionID, AgentID: "agent-att", RequestKey: "req-att",
		Actor: []byte(`{}`), Input: []byte(`{}`), ReplyAddress: []byte(`{}`), EnqueueSeq: 0,
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}

	file := filepath.Join(t.TempDir(), "report.bin")
	if err := os.WriteFile(file, []byte("DM-ORIGINAL-BYTES"), 0o600); err != nil {
		t.Fatal(err)
	}
	coord := &Coordinator{store: cfgstore.NewDBStore(db)}
	res := &agentrun.Result{SessionID: sessionID}
	err = runExecutor{c: coord}.prepareReply(ctx, r,
		agentrun.ReplyAddress{V: 1, ChannelID: "ch-att", AccountKey: "bot", ChatKey: "chat-1"},
		[]pkgchannel.Event{
			{Text: "here is the file"},
			{File: &pkgchannel.FileEvent{Path: file, Name: "report.bin"}},
		}, res)
	if err != nil {
		t.Fatalf("prepareReply: %v", err)
	}
	if len(res.Ops) != 2 {
		t.Fatalf("ops = %d, want text + attachment", len(res.Ops))
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := choutbox.New(db).Append(ctx, tx, res.Ops); err != nil {
		t.Fatalf("append ops: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// The workspace source mutates, then disappears — the committed op chain
	// is now the only copy.
	if err := os.WriteFile(file, []byte("REWRITTEN"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}

	sender := &attachmentSender{handler: &Coordinator{db: db}}
	sent := 0
	for range 4 {
		n, err := choutbox.New(db).ProcessDue(ctx, "ch-att", "", sender)
		if err != nil {
			t.Fatalf("ProcessDue: %v", err)
		}
		sent += n
		if n == 0 {
			break
		}
	}
	if sent != 2 {
		t.Fatalf("sent ops = %d, want text + attachment", sent)
	}
	got := sender.attachments[choutbox.DeliveryKeyForRun(r.ID)]
	if string(got) != "DM-ORIGINAL-BYTES" {
		t.Fatalf("delivered bytes = %q, want committed DM-ORIGINAL-BYTES", got)
	}
}
