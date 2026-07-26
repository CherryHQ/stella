package channel

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/eventlog"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// newReceiptTestGroup returns a pool plus a real ctx_group_state id, which the
// receipt's foreign key requires.
func newReceiptTestGroup(t *testing.T, platformGroupID string) (*pgxpool.Pool, string) {
	t.Helper()
	db := dbtest.New(t)
	groupID, err := eventlog.NewStore(db).ResolveGroupID(context.Background(), "telegram", platformGroupID, "")
	if err != nil {
		t.Fatalf("resolve group: %v", err)
	}
	return db, groupID
}

// TestGroupNewRedeliveryRotatesOnce is the property F3 exists for. `/new` is
// answered before the event-log append, so it never gets the append's
// (group_id, platform_message_id) dedup; without the receipt a redelivered
// command would rotate a second time and silently archive everything the group
// said in between.
//
// Both group entry points reach rotation through rotateGroupChat, differing
// only in where the message id comes from, so the platform message id and the
// Web client_message_id are the two cases here.
func TestGroupNewRedeliveryRotatesOnce(t *testing.T) {
	const groupID = "11111111-1111-4111-8111-111111111111"
	for _, tc := range []struct {
		name      string
		platform  string
		messageID string
	}{
		{name: "platform message id", platform: "telegram", messageID: "tg-msg-7"},
		{name: "web client message id", platform: webGroupPlatform, messageID: "client-7"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, receiptGroupID := newReceiptTestGroup(t, "grp-"+tc.messageID)
			d := &GroupDispatcher{q: sqlc.New(db)}
			rc := newCompactTestChat(t, groupID, auth.User{})
			ctx := context.Background()

			receipt := newCommandReceipt(sqlc.New(db), receiptGroupID, tc.platform, tc.messageID, newSessionCommand)
			if reply := d.rotateGroupChat(ctx, rc, receipt); reply != pkgchannel.NewSessionStartedMessage {
				t.Fatalf("first /new reply = %q, want %q", reply, pkgchannel.NewSessionStartedMessage)
			}
			rotated, err := rc.CurrentSessionForRotation(ctx)
			if err != nil {
				t.Fatalf("resolve session: %v", err)
			}

			// The redelivery carries the same message id and arrives after the group
			// has kept talking, so the "already archived" CAS guard would not catch
			// it — only the receipt does.
			if reply := d.rotateGroupChat(ctx, rc, receipt); reply != pkgchannel.SessionAlreadyResetMessage {
				t.Fatalf("redelivered /new reply = %q, want %q", reply, pkgchannel.SessionAlreadyResetMessage)
			}
			after, err := rc.CurrentSessionForRotation(ctx)
			if err != nil {
				t.Fatalf("resolve session after redelivery: %v", err)
			}
			if after.ID != rotated.ID {
				t.Fatalf("a redelivered /new rotated again: %q -> %q", rotated.ID, after.ID)
			}

			// A genuinely new command is a different message and still rotates.
			next := newCommandReceipt(sqlc.New(db), receiptGroupID, tc.platform, tc.messageID+"-b", newSessionCommand)
			if reply := d.rotateGroupChat(ctx, rc, next); reply != pkgchannel.NewSessionStartedMessage {
				t.Fatalf("second command reply = %q, want a fresh rotation", reply)
			}
		})
	}
}

// TestGroupNewReceiptIsInertWithoutAMessageID covers deliveries Stella cannot
// name. Storing a blank id would collapse every such message onto one row and
// make the first `/new` in a group permanently suppress the rest.
func TestGroupNewReceiptIsInertWithoutAMessageID(t *testing.T) {
	const groupID = "11111111-1111-4111-8111-111111111111"
	db, receiptGroupID := newReceiptTestGroup(t, "grp-noid")
	d := &GroupDispatcher{q: sqlc.New(db)}
	rc := newCompactTestChat(t, groupID, auth.User{})
	ctx := context.Background()

	for i := range 2 {
		receipt := newCommandReceipt(sqlc.New(db), receiptGroupID, "telegram", "", newSessionCommand)
		if reply := d.rotateGroupChat(ctx, rc, receipt); reply != pkgchannel.NewSessionStartedMessage {
			t.Fatalf("/new #%d reply = %q, want a rotation", i, reply)
		}
	}
	var count int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM channel_group_command_receipt`).Scan(&count); err != nil {
		t.Fatalf("count receipts: %v", err)
	}
	if count != 0 {
		t.Fatalf("receipts written for an unidentifiable message = %d, want 0", count)
	}
}

// TestGroupNewReceiptReleasedWhenRotationNeverRan keeps a failure from costing
// the group its command: nothing was reset, so the next delivery of the same
// message must be allowed to try again.
func TestGroupNewReceiptReleasedWhenRotationNeverRan(t *testing.T) {
	const groupID = "11111111-1111-4111-8111-111111111111"
	db, receiptGroupID := newReceiptTestGroup(t, "grp-release")
	d := &GroupDispatcher{q: sqlc.New(db)}
	ctx := context.Background()

	// Authorization fails, so rotation is abandoned after the claim.
	rc := newCompactTestChat(t, groupID, auth.User{})
	rc.Service.SessionAccess = compactSessionAccessSvc{reg: rc.Service.Sessions, useErr: errors.New("denied")}
	receipt := newCommandReceipt(sqlc.New(db), receiptGroupID, "telegram", "m-fail", newSessionCommand)
	if reply := d.rotateGroupChat(ctx, rc, receipt); reply == pkgchannel.NewSessionStartedMessage {
		t.Fatalf("reply = %q, want a failure", reply)
	}

	var count int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM channel_group_command_receipt`).Scan(&count); err != nil {
		t.Fatalf("count receipts: %v", err)
	}
	if count != 0 {
		t.Fatalf("a failed /new left %d claims behind, want 0", count)
	}
}

// TestWebGroupNewCarriesClientMessageID connects the Web half of F3. The command
// is answered before the event-log append and therefore misses the append's
// dedup; the browser's idempotency token is the only thing that can identify a
// retried send, so PrepareSend has to hand it to the rotation.
func TestWebGroupNewCarriesClientMessageID(t *testing.T) {
	fx := setupGroupFixture(t)
	ctx := fx.ts.ctx()
	owner := createTestUser(t, fx.ts.oidcStore, "web-new@example.com")
	acc := fx.begin(t, owner.ID, false)
	g, err := acc.Create(ctx, "team", []string{fx.stella})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	prep, err := acc.PrepareSend(ctx, g.ID, "/new", "client-msg-1")
	if err != nil {
		t.Fatalf("PrepareSend: %v", err)
	}
	if !prep.Command || prep.Reply != pkgchannel.NewSessionStartedMessage {
		t.Fatalf("prepared send = %+v, want an intercepted /new reply", prep)
	}
	if got := fx.runner.rotateMsgs; len(got) != 1 || got[0] != "client-msg-1" {
		t.Fatalf("rotation client message ids = %v, want [client-msg-1]", got)
	}
}
