package channel

import (
	"context"
	"testing"

	"github.com/CherryHQ/stella/internal/db/dbtest"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestInboundMessageReceiptSuppressesRedeliveryAcrossProcesses(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	receipt := inboundReceiptForMessage(sqlc.New(db), pkgchannel.IncomingMessage{
		Platform:  "feishu",
		ChannelID: "feishu-work",
		ChatID:    "oc_chat",
		MessageID: "om_message",
	})

	claimed, err := receipt.claim(ctx)
	if err != nil || !claimed {
		t.Fatalf("first claim = %v, %v; want true, nil", claimed, err)
	}
	claimed, err = receipt.claim(ctx)
	if err != nil || claimed {
		t.Fatalf("redelivery claim = %v, %v; want false, nil", claimed, err)
	}

	// The physical channel instance is part of the key. Another app is not a
	// replay even when the platform happens to reuse the same message id.
	other := inboundReceiptForMessage(sqlc.New(db), pkgchannel.IncomingMessage{
		Platform:  "feishu",
		ChannelID: "feishu-other",
		ChatID:    "oc_chat",
		MessageID: "om_message",
	})
	claimed, err = other.claim(ctx)
	if err != nil || !claimed {
		t.Fatalf("other channel claim = %v, %v; want true, nil", claimed, err)
	}
}

func TestInboundMessageReceiptExpiresAndCanBeReleased(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	receipt := inboundReceiptForMessage(sqlc.New(db), pkgchannel.IncomingMessage{
		Platform: "feishu", ChannelID: "feishu-work", ChatID: "oc_chat", MessageID: "om_message",
	})
	if claimed, err := receipt.claim(ctx); err != nil || !claimed {
		t.Fatalf("initial claim = %v, %v", claimed, err)
	}
	if _, err := db.Exec(ctx, `UPDATE channel_inbound_message_receipt SET expires_at = now() - interval '1 second'`); err != nil {
		t.Fatalf("expire receipt: %v", err)
	}
	if claimed, err := receipt.claim(ctx); err != nil || !claimed {
		t.Fatalf("expired claim = %v, %v; want true, nil", claimed, err)
	}
	receipt.release(ctx)
	if claimed, err := receipt.claim(ctx); err != nil || !claimed {
		t.Fatalf("released claim = %v, %v; want true, nil", claimed, err)
	}
}
