package channel

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// A group reply's sends are authorized by the dispatch row that owns it, so a
// superseded attempt cannot keep sending chunks the row no longer owns.
func TestGroupPublishAuthorityFollowsTheDispatchRow(t *testing.T) {
	for _, tc := range []struct {
		name string
		// attempt is the attempt_count this handle was created for.
		attempt int64
		// claimedAttempt is the attempt that actually claimed the row, if any.
		claimedAttempt int64
		claimed        bool
		published      bool
		wantRefused    bool
	}{
		{name: "owning attempt may send", attempt: 1, claimed: true, claimedAttempt: 1},
		{name: "already published", attempt: 1, claimed: true, claimedAttempt: 1, published: true, wantRefused: true},
		{name: "never claimed the row", attempt: 1, wantRefused: true},
		{name: "superseded by a newer attempt", attempt: 1, claimed: true, claimedAttempt: 2, wantRefused: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx := newDispatcherFixture(t, "web", "{}")
			ctx := t.Context()
			q := sqlc.New(fx.db)
			const dispatchID = "d15a0000-0000-0000-0000-0000000009f1"
			insertGroupDispatch(t, fx.db, dispatchID, fx.message.ID, fx.groupID, "agent-1", "running", tc.claimedAttempt, pgtype.Timestamptz{})
			if tc.claimed {
				if _, err := q.MarkGroupDispatchPublishStarted(ctx, sqlc.MarkGroupDispatchPublishStartedParams{ID: dispatchID, AttemptCount: tc.claimedAttempt}); err != nil {
					t.Fatalf("mark publish started: %v", err)
				}
			}
			if tc.published {
				if _, err := q.MarkGroupDispatchPublished(ctx, sqlc.MarkGroupDispatchPublishedParams{ID: dispatchID, AttemptCount: tc.claimedAttempt, ResultMessageID: ""}); err != nil {
					t.Fatalf("mark published: %v", err)
				}
			}

			delivery := newDispatchDelivery(q, dispatchID, tc.attempt)
			err := delivery.Authorize(ctx, pkgchannel.SendOutput)
			if tc.wantRefused {
				if !errors.Is(err, errDeliveryUnauthorized) {
					t.Fatalf("authorize = %v, want a refusal", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("authorize = %v, want authorized", err)
			}
			if err := delivery.Settle(ctx, pkgchannel.DeliveryNotSent); err != nil {
				t.Fatalf("settle: %v", err)
			}
			if err := delivery.Authorize(ctx, pkgchannel.SendOutput); !errors.Is(err, errDeliverySettled) {
				t.Fatalf("settled group attempt authorized another request: %v", err)
			}
			select {
			case <-delivery.Done():
			default:
				t.Fatal("settling a group attempt did not release the per-group FIFO")
			}
		})
	}
}
