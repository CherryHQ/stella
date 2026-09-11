package channel

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// The receipt's claim/release contract is the once-per-message guard behind
// destructive commands on the durable route: claim reserves execution,
// release hands it back when the command provably never ran, and an
// unidentifiable message fails closed instead of running unguarded.

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

// The second claim of the same message is refused: a redelivery must never
// run the destructive command again.
func TestReceiptClaimsOnce(t *testing.T) {
	db := dbtest.New(t)
	receipt := newDMReceipt(db, "m-1")
	claimed, err := receipt.claim(context.Background())
	if err != nil || !claimed {
		t.Fatalf("first claim = %v, %v", claimed, err)
	}
	claimed, err = receipt.claim(context.Background())
	if err != nil || claimed {
		t.Fatalf("second claim = %v, %v, want refused", claimed, err)
	}
	if n := countChatReceipts(t, db); n != 1 {
		t.Fatalf("receipts = %d, want 1", n)
	}
}

// release hands the claim back: a delivery that provably never ran may retry.
func TestReceiptReleasePermitsRetry(t *testing.T) {
	db := dbtest.New(t)
	receipt := newDMReceipt(db, "m-2")
	if claimed, err := receipt.claim(context.Background()); err != nil || !claimed {
		t.Fatalf("first claim = %v, %v", claimed, err)
	}
	receipt.release(context.Background())
	if claimed, err := receipt.claim(context.Background()); err != nil || !claimed {
		t.Fatalf("claim after release = %v, %v, want granted", claimed, err)
	}
}

// A message with no stable identity can never claim: errUnidentifiedCommand
// makes the caller answer "unverifiable" instead of running unguarded.
func TestReceiptFailsClosedWithoutMessageID(t *testing.T) {
	db := dbtest.New(t)
	receipt := newDMReceipt(db, "")
	claimed, err := receipt.claim(context.Background())
	if err == nil || claimed {
		t.Fatalf("unidentified claim = %v, %v, want refused", claimed, err)
	}
}
