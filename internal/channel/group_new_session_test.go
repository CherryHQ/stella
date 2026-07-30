package channel

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/authz"
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

// TestGroupNewFailsClosedWithoutAMessageID covers deliveries Stella cannot
// name: without an id a redelivery of a `/new` that already ran is
// indistinguishable from a new command, so the destructive reset refuses to
// run instead of running unguarded.
func TestGroupNewFailsClosedWithoutAMessageID(t *testing.T) {
	const groupID = "11111111-1111-4111-8111-111111111111"
	db, receiptGroupID := newReceiptTestGroup(t, "grp-noid")
	d := &GroupDispatcher{q: sqlc.New(db)}
	rc := newCompactTestChat(t, groupID, auth.User{})
	ctx := context.Background()

	receipt := newCommandReceipt(sqlc.New(db), receiptGroupID, "telegram", "", newSessionCommand)
	if reply := d.rotateGroupChat(ctx, rc, receipt); reply != pkgchannel.NewSessionUnverifiableMessage {
		t.Fatalf("/new without a message id reply = %q, want %q", reply, pkgchannel.NewSessionUnverifiableMessage)
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

// rotateFailAccessSvc lets the session resolve succeed while the rotation
// itself fails with a chosen error — the seam the receipt's release contract
// distinguishes on.
type rotateFailAccessSvc struct {
	reg       *session.Registry
	rotateErr error
}

func (s rotateFailAccessSvc) Begin(context.Context, authz.Authority) (agent.SessionAccess, error) {
	return rotateFailAccess{compactSessionAccess{reg: s.reg}, s.rotateErr}, nil
}

type rotateFailAccess struct {
	compactSessionAccess
	rotateErr error
}

func (a rotateFailAccess) RotateChannel(context.Context, session.ChannelRequest) (session.Info, error) {
	return session.Info{}, a.rotateErr
}

// TestGroupNewReceiptReleasedWhenRotationFails pins the release decision to the
// rotation OUTCOME, not to whether the queued callback returned an error:
// RotateInfo is one transaction, so a non-stale failure means nothing was
// archived and the same message must be allowed to try again. Folding the error
// into the reply string (as NewSessionReply does) would strand the claim.
func TestGroupNewReceiptReleasedWhenRotationFails(t *testing.T) {
	const groupID = "11111111-1111-4111-8111-111111111111"
	db, receiptGroupID := newReceiptTestGroup(t, "grp-rotatefail")
	d := &GroupDispatcher{q: sqlc.New(db)}
	ctx := context.Background()

	rc := newCompactTestChat(t, groupID, auth.User{})
	rc.Service.SessionAccess = rotateFailAccessSvc{reg: rc.Service.Sessions, rotateErr: errors.New("db down")}
	receipt := newCommandReceipt(sqlc.New(db), receiptGroupID, "telegram", "m-rotatefail", newSessionCommand)
	if reply := d.rotateGroupChat(ctx, rc, receipt); reply == pkgchannel.NewSessionStartedMessage {
		t.Fatalf("reply = %q, want a failure", reply)
	}

	var count int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM channel_group_command_receipt`).Scan(&count); err != nil {
		t.Fatalf("count receipts: %v", err)
	}
	if count != 0 {
		t.Fatalf("a rotation that never ran left %d claims behind, want 0", count)
	}
}

// TestGroupNewReceiptKeptWhenRotationWasStale is the other half of that
// contract: a stale CAS means another /new already performed this message's
// reset, so the claim must stand — releasing it would let a redelivery rotate
// the successor.
func TestGroupNewReceiptKeptWhenRotationWasStale(t *testing.T) {
	const groupID = "11111111-1111-4111-8111-111111111111"
	db, receiptGroupID := newReceiptTestGroup(t, "grp-stale")
	d := &GroupDispatcher{q: sqlc.New(db)}
	ctx := context.Background()

	rc := newCompactTestChat(t, groupID, auth.User{})
	rc.Service.SessionAccess = rotateFailAccessSvc{reg: rc.Service.Sessions, rotateErr: session.ErrStaleRotation}
	receipt := newCommandReceipt(sqlc.New(db), receiptGroupID, "telegram", "m-stale", newSessionCommand)
	if reply := d.rotateGroupChat(ctx, rc, receipt); reply != pkgchannel.SessionAlreadyResetMessage {
		t.Fatalf("reply = %q, want %q", reply, pkgchannel.SessionAlreadyResetMessage)
	}

	var count int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM channel_group_command_receipt`).Scan(&count); err != nil {
		t.Fatalf("count receipts: %v", err)
	}
	if count != 1 {
		t.Fatalf("an already-done reset kept %d claims, want 1", count)
	}
}

// TestGroupNewReceiptKeptWhenCommitOutcomeUnknown covers the third ambiguous
// shape: the rotation's COMMIT was sent but the acknowledgement was lost, so
// the server may have committed. RotateInfo marks exactly that case with
// ErrRotationOutcomeUnknown, and the error alone is not an outcome — the
// session binding is, since a rotation moves it and it never moves back. Each
// answer the binding can give leads to a different report AND a different
// receipt decision.
//
// lostAckAccessSvc is the seam: the rotation may land before the
// acknowledgement is lost, and the verification read may itself fail.
type lostAckAccessSvc struct {
	reg        *session.Registry
	commit     bool
	resolveErr error // injected into the verification read only, after rotating
	rotated    *bool
}

func (s lostAckAccessSvc) Begin(context.Context, authz.Authority) (agent.SessionAccess, error) {
	return lostAckAccess{compactSessionAccess{reg: s.reg}, s.commit, s.resolveErr, s.rotated}, nil
}

type lostAckAccess struct {
	compactSessionAccess
	commit     bool
	resolveErr error
	rotated    *bool
}

func (a lostAckAccess) RotateChannel(ctx context.Context, req session.ChannelRequest) (session.Info, error) {
	*a.rotated = true
	if a.commit {
		if _, err := a.compactSessionAccess.RotateChannel(ctx, req); err != nil {
			return session.Info{}, err
		}
	}
	return session.Info{}, fmt.Errorf("connection reset before response: %w", session.ErrRotationOutcomeUnknown)
}

func (a lostAckAccess) ResolveChatChannel(ctx context.Context, req session.ChannelRequest) (session.Info, error) {
	// The pre-rotation resolve must still work: only the verification read is
	// under test here.
	if a.resolveErr != nil && *a.rotated {
		return session.Info{}, a.resolveErr
	}
	return a.compactSessionAccess.ResolveChatChannel(ctx, req)
}

func TestGroupNewVerifiesUnknownCommitOutcome(t *testing.T) {
	const groupID = "11111111-1111-4111-8111-111111111111"
	for _, tc := range []struct {
		name         string
		access       lostAckAccessSvc
		wantReply    string
		wantReceipts int
	}{
		{
			// The binding moved: the transaction committed and only the
			// acknowledgement was lost, so this is a success, and the claim must
			// stand or a redelivery would rotate the fresh session away.
			name:         "binding moved is a success",
			access:       lostAckAccessSvc{commit: true},
			wantReply:    pkgchannel.NewSessionStartedMessage,
			wantReceipts: 1,
		},
		{
			// The binding never moved, which proves the rollback. Nothing was
			// reset, so the claim has nothing to protect and the same message
			// must be allowed to try again.
			name:         "binding unchanged was not executed",
			access:       lostAckAccessSvc{commit: false},
			wantReply:    pkgchannel.NewSessionNotExecutedMessage,
			wantReceipts: 0,
		},
		{
			// Nothing can answer: the reply must say so instead of inviting a
			// retry, and the claim stays because the reset may have happened.
			name:         "unreadable binding stays uncertain",
			access:       lostAckAccessSvc{commit: true, resolveErr: errors.New("db unreachable")},
			wantReply:    pkgchannel.NewSessionOutcomeUnknownMessage,
			wantReceipts: 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, receiptGroupID := newReceiptTestGroup(t, "grp-unknown-"+tc.name)
			d := &GroupDispatcher{q: sqlc.New(db)}
			ctx := context.Background()

			rc := newCompactTestChat(t, groupID, auth.User{})
			rotated := false
			tc.access.reg = rc.Service.Sessions
			tc.access.rotated = &rotated
			rc.Service.SessionAccess = tc.access

			receipt := newCommandReceipt(sqlc.New(db), receiptGroupID, "telegram", "m-"+tc.name, newSessionCommand)
			if reply := d.rotateGroupChat(ctx, rc, receipt); reply != tc.wantReply {
				t.Fatalf("reply = %q, want %q", reply, tc.wantReply)
			}

			var count int
			if err := db.QueryRow(ctx, `SELECT count(*) FROM channel_group_command_receipt`).Scan(&count); err != nil {
				t.Fatalf("count receipts: %v", err)
			}
			if count != tc.wantReceipts {
				t.Fatalf("receipts = %d, want %d", count, tc.wantReceipts)
			}
		})
	}
}

// cancelThenRotateAccessSvc simulates the round-3 race: the rotation commits in
// the same instant the request context dies. The rotation itself runs on a
// background context to guarantee the commit, exactly like a transaction that
// landed as the caller gave up.
type cancelThenRotateAccessSvc struct {
	reg    *session.Registry
	cancel context.CancelFunc
}

func (s cancelThenRotateAccessSvc) Begin(context.Context, authz.Authority) (agent.SessionAccess, error) {
	return cancelThenRotateAccess{compactSessionAccess{reg: s.reg}, s.cancel}, nil
}

type cancelThenRotateAccess struct {
	compactSessionAccess
	cancel context.CancelFunc
}

func (a cancelThenRotateAccess) RotateChannel(_ context.Context, req session.ChannelRequest) (session.Info, error) {
	a.cancel()
	return a.reg.RotateChannel(context.Background(), req)
}

// TestGroupNewReceiptKeptWhenCancelRacesCommit runs the production path (real
// queue) through the round-3 race: rotation commits while the request context
// dies, so EnqueueControl may resolve either way. Whichever reply comes back,
// the claim must stand — releasing it would let the redelivery rotate the
// successor and wipe everything said since.
func TestGroupNewReceiptKeptWhenCancelRacesCommit(t *testing.T) {
	const groupID = "11111111-1111-4111-8111-111111111111"
	db, receiptGroupID := newReceiptTestGroup(t, "grp-ambig")
	d := &GroupDispatcher{q: sqlc.New(db), queue: newSessionQueue()}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rc := newCompactTestChat(t, groupID, auth.User{})
	rc.Service.SessionAccess = cancelThenRotateAccessSvc{reg: rc.Service.Sessions, cancel: cancel}
	before, err := rc.CurrentSessionForRotation(context.Background())
	if err != nil {
		t.Fatalf("resolve session: %v", err)
	}

	receipt := newCommandReceipt(sqlc.New(db), receiptGroupID, "telegram", "m-ambig", newSessionCommand)
	_ = d.rotateGroupChat(ctx, rc, receipt) // either reply is legitimate here

	rotated, err := rc.CurrentSessionForRotation(context.Background())
	if err != nil {
		t.Fatalf("resolve session after rotation: %v", err)
	}
	if rotated.ID == before.ID {
		t.Fatal("rotation did not commit; the race this test drives never happened")
	}
	var count int
	if err := db.QueryRow(context.Background(), `SELECT count(*) FROM channel_group_command_receipt`).Scan(&count); err != nil {
		t.Fatalf("count receipts: %v", err)
	}
	if count != 1 {
		t.Fatalf("a committed rotation kept %d claims, want 1", count)
	}

	// The redelivery must answer, not rotate the successor.
	rc.Service.SessionAccess = compactSessionAccessSvc{reg: rc.Service.Sessions}
	if reply := d.rotateGroupChat(context.Background(), rc, receipt); reply != pkgchannel.SessionAlreadyResetMessage {
		t.Fatalf("redelivered /new reply = %q, want %q", reply, pkgchannel.SessionAlreadyResetMessage)
	}
	after, err := rc.CurrentSessionForRotation(context.Background())
	if err != nil {
		t.Fatalf("resolve session after redelivery: %v", err)
	}
	if after.ID != rotated.ID {
		t.Fatalf("a redelivered /new rotated the successor: %q -> %q", rotated.ID, after.ID)
	}
}

// TestGroupNewReceiptReleasedWhenQueueNeverRan covers the other side of the
// started contract on the production path: the caller gives up while the slot
// is held by an in-flight operation, the rotation provably never starts, and
// the claim must be dropped so the user's retry works.
func TestGroupNewReceiptReleasedWhenQueueNeverRan(t *testing.T) {
	const groupID = "11111111-1111-4111-8111-111111111111"
	db, receiptGroupID := newReceiptTestGroup(t, "grp-neverran")
	d := &GroupDispatcher{q: sqlc.New(db), queue: newSessionQueue()}
	rc := newCompactTestChat(t, groupID, auth.User{})
	before, err := rc.CurrentSessionForRotation(context.Background())
	if err != nil {
		t.Fatalf("resolve session: %v", err)
	}

	blockerRunning := make(chan struct{})
	unblock := make(chan struct{})
	blockerDone := make(chan struct{})
	go func() {
		defer close(blockerDone)
		_, _ = d.queue.EnqueueControl(context.Background(), rc.SessionKey, func(context.Context) error {
			close(blockerRunning)
			<-unblock
			return nil
		})
	}()
	<-blockerRunning

	// Generous for the claim and resolve (local, sub-millisecond), far shorter
	// than the blocker: the deadline can only fire while waiting on the queue.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	receipt := newCommandReceipt(sqlc.New(db), receiptGroupID, "telegram", "m-neverran", newSessionCommand)
	if reply := d.rotateGroupChat(ctx, rc, receipt); reply == pkgchannel.NewSessionStartedMessage {
		t.Fatalf("reply = %q, want a failure", reply)
	}
	close(unblock)
	<-blockerDone

	var count int
	if err := db.QueryRow(context.Background(), `SELECT count(*) FROM channel_group_command_receipt`).Scan(&count); err != nil {
		t.Fatalf("count receipts: %v", err)
	}
	if count != 0 {
		t.Fatalf("a rotation that never started left %d claims, want 0", count)
	}
	// Drain the abandoned request, then prove the rotation really never ran.
	if _, err := d.queue.EnqueueControl(context.Background(), rc.SessionKey, func(context.Context) error { return nil }); err != nil {
		t.Fatalf("follow-up control op: %v", err)
	}
	after, err := rc.CurrentSessionForRotation(context.Background())
	if err != nil {
		t.Fatalf("resolve session after abandoned /new: %v", err)
	}
	if after.ID != before.ID {
		t.Fatalf("an abandoned /new rotated the session: %q -> %q", before.ID, after.ID)
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
