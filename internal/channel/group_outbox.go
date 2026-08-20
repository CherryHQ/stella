package channel

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// GroupOutboxEnvelope stores dispatch metadata that must be decided at ingest
// time, while the group_id and membership view are available in the append
// transaction.
type GroupOutboxEnvelope struct {
	NudgeTarget       string               `json:"nudge_target,omitempty"`
	Mentions          []pkgchannel.Mention `json:"mentions,omitempty"`
	LifecycleFeedback bool                 `json:"lifecycle_feedback,omitempty"`
}

func EncodeGroupOutboxEnvelope(mentions []pkgchannel.Mention) (string, error) {
	return encodeGroupOutboxEnvelope(GroupOutboxEnvelope{Mentions: mentions})
}

func EncodeGroupOutboxEnvelopeWithFeedback(mentions []pkgchannel.Mention, lifecycleFeedback bool) (string, error) {
	return encodeGroupOutboxEnvelope(GroupOutboxEnvelope{Mentions: mentions, LifecycleFeedback: lifecycleFeedback})
}

func encodeGroupOutboxEnvelope(envelope GroupOutboxEnvelope) (string, error) {
	data, err := json.Marshal(envelope)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func DecodeGroupOutboxEnvelope(raw string) (GroupOutboxEnvelope, error) {
	if raw == "" {
		return GroupOutboxEnvelope{}, nil
	}
	var envelope GroupOutboxEnvelope
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		return GroupOutboxEnvelope{}, err
	}
	return envelope, nil
}

// createPendingGroupOutbox is the only way canonical ingest and nudge appends
// create a claimable outbox row. The zero-value lease and retry timestamps
// deliberately mean immediately claimable work.
func createPendingGroupOutbox(ctx context.Context, q *sqlc.Queries, groupMessageID, groupID, envelope string) error {
	_, err := q.CreateGroupOutbox(ctx, sqlc.CreateGroupOutboxParams{
		ID:             uuid.Must(uuid.NewV7()).String(),
		GroupMessageID: groupMessageID,
		GroupID:        groupID,
		Envelope:       envelope,
		Status:         "pending",
		AttemptCount:   0,
		LeaseUntil:     pgtype.Timestamptz{},
		NextAttemptAt:  pgtype.Timestamptz{},
		LastError:      "",
	})
	return err
}
