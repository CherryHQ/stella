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
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// The receipt's release-or-keep decision is the part of `/new` that a wrong
// answer makes destructive: released when the reset actually happened, a
// redelivery rotates the successor and wipes everything said since. These
// tests drive rotateChatSession directly over the DM receipt, which is the
// only receipt left now that a group's shared session cannot be reset.

// newReceiptTestChat returns an unlinked private-channel chat — the DM shape
// that rotates through RotateChannel, which is the seam the stubs below
// replace.
func newReceiptTestChat(t *testing.T) *ResolvedChat {
	t.Helper()
	return newCompactTestChat(t, "", auth.User{ID: "receipt-user", Role: auth.RoleUser})
}

// newDMReceipt builds a claimable DM receipt over a real table.
func newDMReceipt(db *pgxpool.Pool, messageID string) chatCommandReceipt {
	return chatCommandReceipt{
		q:         sqlc.New(db),
		channelID: "tg-bot-a",
		chatKey:   "tg-acct-1",
		messageID: messageID,
		command:   newSessionCommand,
		binding:   "receipt-binding",
	}
}

func countChatReceipts(t *testing.T, db *pgxpool.Pool) int {
	t.Helper()
	var count int
	if err := db.QueryRow(context.Background(),
		`SELECT count(*) FROM channel_chat_command_receipt`).Scan(&count); err != nil {
		t.Fatalf("count receipts: %v", err)
	}
	return count
}

// TestNewReceiptReleasedWhenRotationNeverRan keeps a failure from costing the
// user their command: nothing was reset, so the next delivery of the same
// message must be allowed to try again.
func TestNewReceiptReleasedWhenRotationNeverRan(t *testing.T) {
	db := dbtest.New(t)
	ctx := context.Background()

	// Authorization fails, so rotation is abandoned after the claim.
	rc := newReceiptTestChat(t)
	rc.Service.SessionAccess = compactSessionAccessSvc{reg: rc.Service.Sessions, useErr: errors.New("denied")}
	if reply := rotateChatSession(ctx, rc, newDMReceipt(db, "m-fail"), nil); reply == pkgchannel.NewSessionStartedMessage {
		t.Fatalf("reply = %q, want a failure", reply)
	}
	if count := countChatReceipts(t, db); count != 0 {
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

// TestNewReceiptReleasedWhenRotationFails pins the release decision to the
// rotation OUTCOME, not to whether the queued callback returned an error:
// RotateInfo is one transaction, so a non-stale failure means nothing was
// archived and the same message must be allowed to try again. Folding the error
// into the reply string (as NewSessionReply does) would strand the claim.
func TestNewReceiptReleasedWhenRotationFails(t *testing.T) {
	db := dbtest.New(t)
	ctx := context.Background()

	rc := newReceiptTestChat(t)
	rc.Service.SessionAccess = rotateFailAccessSvc{reg: rc.Service.Sessions, rotateErr: errors.New("db down")}
	if reply := rotateChatSession(ctx, rc, newDMReceipt(db, "m-rotatefail"), nil); reply == pkgchannel.NewSessionStartedMessage {
		t.Fatalf("reply = %q, want a failure", reply)
	}
	if count := countChatReceipts(t, db); count != 0 {
		t.Fatalf("a rotation that never ran left %d claims behind, want 0", count)
	}
}

// TestNewReceiptKeptWhenRotationWasStale is the other half of that contract: a
// stale CAS means another `/new` already performed this message's reset, so the
// claim must stand — releasing it would let a redelivery rotate the successor.
func TestNewReceiptKeptWhenRotationWasStale(t *testing.T) {
	db := dbtest.New(t)
	ctx := context.Background()

	rc := newReceiptTestChat(t)
	rc.Service.SessionAccess = rotateFailAccessSvc{reg: rc.Service.Sessions, rotateErr: session.ErrStaleRotation}
	if reply := rotateChatSession(ctx, rc, newDMReceipt(db, "m-stale"), nil); reply != pkgchannel.SessionAlreadyResetMessage {
		t.Fatalf("reply = %q, want %q", reply, pkgchannel.SessionAlreadyResetMessage)
	}
	if count := countChatReceipts(t, db); count != 1 {
		t.Fatalf("an already-done reset kept %d claims, want 1", count)
	}
}

// lostAckAccessSvc is the seam for the third ambiguous shape: the rotation's
// COMMIT was sent but the acknowledgement was lost, so the server may have
// committed. RotateInfo marks exactly that case with ErrRotationOutcomeUnknown,
// and the error alone is not an outcome — the session binding is, since a
// rotation moves it and it never moves back. The rotation may land before the
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

// TestNewVerifiesUnknownCommitOutcome proves each answer the binding can give
// leads to a different report AND a different receipt decision.
func TestNewVerifiesUnknownCommitOutcome(t *testing.T) {
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
			db := dbtest.New(t)
			ctx := context.Background()

			rc := newReceiptTestChat(t)
			rotated := false
			tc.access.reg = rc.Service.Sessions
			tc.access.rotated = &rotated
			rc.Service.SessionAccess = tc.access

			if reply := rotateChatSession(ctx, rc, newDMReceipt(db, "m-unknown"), nil); reply != tc.wantReply {
				t.Fatalf("reply = %q, want %q", reply, tc.wantReply)
			}
			if count := countChatReceipts(t, db); count != tc.wantReceipts {
				t.Fatalf("receipts = %d, want %d", count, tc.wantReceipts)
			}
		})
	}
}

// cancelThenRotateAccessSvc simulates the race where the rotation commits in
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

// TestNewReceiptKeptWhenCancelRacesCommit runs the production path (real queue)
// through that race: rotation commits while the request context dies, so
// EnqueueControl may resolve either way. Whichever reply comes back, the claim
// must stand — releasing it would let the redelivery rotate the successor and
// wipe everything said since.
func TestNewReceiptKeptWhenCancelRacesCommit(t *testing.T) {
	db := dbtest.New(t)
	queue := newSessionQueue()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rc := newReceiptTestChat(t)
	rc.Service.SessionAccess = cancelThenRotateAccessSvc{reg: rc.Service.Sessions, cancel: cancel}
	before, err := rc.CurrentSessionForRotation(context.Background())
	if err != nil {
		t.Fatalf("resolve session: %v", err)
	}

	receipt := newDMReceipt(db, "m-ambig")
	_ = rotateChatSession(ctx, rc, receipt, queue) // either reply is legitimate here

	rotated, err := rc.CurrentSessionForRotation(context.Background())
	if err != nil {
		t.Fatalf("resolve session after rotation: %v", err)
	}
	if rotated.ID == before.ID {
		t.Fatal("rotation did not commit; the race this test drives never happened")
	}
	if count := countChatReceipts(t, db); count != 1 {
		t.Fatalf("a committed rotation kept %d claims, want 1", count)
	}

	// The redelivery must answer, not rotate the successor.
	rc.Service.SessionAccess = compactSessionAccessSvc{reg: rc.Service.Sessions}
	if reply := rotateChatSession(context.Background(), rc, receipt, queue); reply != pkgchannel.SessionAlreadyResetMessage {
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

// TestNewReceiptReleasedWhenQueueNeverRan covers the other side of the started
// contract on the production path: the caller gives up while the slot is held
// by an in-flight operation, the rotation provably never starts, and the claim
// must be dropped so the user's retry works.
func TestNewReceiptReleasedWhenQueueNeverRan(t *testing.T) {
	db := dbtest.New(t)
	queue := newSessionQueue()
	rc := newReceiptTestChat(t)
	before, err := rc.CurrentSessionForRotation(context.Background())
	if err != nil {
		t.Fatalf("resolve session: %v", err)
	}

	blockerRunning := make(chan struct{})
	unblock := make(chan struct{})
	blockerDone := make(chan struct{})
	go func() {
		defer close(blockerDone)
		_, _ = queue.EnqueueControl(context.Background(), rc.queueKey(), func(context.Context) error {
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
	if reply := rotateChatSession(ctx, rc, newDMReceipt(db, "m-neverran"), queue); reply == pkgchannel.NewSessionStartedMessage {
		t.Fatalf("reply = %q, want a failure", reply)
	}
	close(unblock)
	<-blockerDone

	if count := countChatReceipts(t, db); count != 0 {
		t.Fatalf("a rotation that never started left %d claims, want 0", count)
	}
	// Drain the abandoned request, then prove the rotation really never ran.
	if _, err := queue.EnqueueControl(context.Background(), rc.queueKey(), func(context.Context) error { return nil }); err != nil {
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
