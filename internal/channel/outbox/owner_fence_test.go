package outbox

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/internal/db/dbtest"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestStaleChannelOwnerCannotClaimAndSend(t *testing.T) {
	db := dbtest.New(t)
	createChannel(t, db, "ch-1")
	q := sqlc.New(db)
	const current = "11111111-1111-4111-8111-111111111111"
	const stale = "22222222-2222-4222-8222-222222222222"
	if _, err := q.ClaimChannelRuntime(t.Context(), sqlc.ClaimChannelRuntimeParams{ChannelID: "ch-1", OwnerID: pgtype.Text{String: "current-owner", Valid: true}, Token: pgtype.Text{String: current, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	s := New(db)
	appendOps(t, s, db, []Op{op("d-1", 0)})
	sender := &fakeSender{results: []sendResult{{receipt: pkgchannel.SendResult{PlatformMessageID: "sent-by-stale-owner"}}}}
	n, err := s.ProcessDue(t.Context(), "ch-1", stale, sender)
	rows, readErr := s.ListByDelivery(t.Context(), "d-1")
	if readErr != nil {
		t.Fatal(readErr)
	}
	t.Logf("attempted=%d SDK_calls=%d state=%s process_error=%v", n, len(sender.calls), rows[0].State, err)
	if len(sender.calls) > 0 || rows[0].State == StateSent {
		t.Fatal("a foreign owner token must not be allowed to claim or send pending operations")
	}
}
