package channel

import (
	"context"
	"encoding/json"
	"fmt"

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
	// ReplyCapabilityRef is an opaque durable capability id. The secret it
	// names stays encrypted in the vault and is resolved only at publish time.
	ReplyCapabilityRef string `json:"reply_capability_ref,omitempty"`
}

func EncodeGroupOutboxEnvelope(mentions []pkgchannel.Mention) (string, error) {
	return encodeGroupOutboxEnvelope(GroupOutboxEnvelope{Mentions: mentions})
}

func EncodeGroupOutboxEnvelopeWithFeedback(mentions []pkgchannel.Mention, lifecycleFeedback bool) (string, error) {
	return encodeGroupOutboxEnvelope(GroupOutboxEnvelope{Mentions: mentions, LifecycleFeedback: lifecycleFeedback})
}

// EncodeGroupOutboxEnvelopeWithCapability persists immutable reply metadata
// alongside the accepted group message. The capability reference is opaque;
// callers must never put the decrypted platform secret in this envelope.
func EncodeGroupOutboxEnvelopeWithCapability(mentions []pkgchannel.Mention, lifecycleFeedback bool, capabilityRef string) (string, error) {
	return encodeGroupOutboxEnvelope(GroupOutboxEnvelope{
		Mentions: mentions, LifecycleFeedback: lifecycleFeedback, ReplyCapabilityRef: capabilityRef,
	})
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
	if err != nil {
		return err
	}
	// The route is created in the same append transaction as the outbox. That
	// makes sequence ordering visible before any worker can observe this
	// message, including when an earlier outbox is still pending.
	message, err := q.GetGroupMessage(ctx, groupMessageID)
	if err != nil {
		return fmt.Errorf("get outbox message for route: %w", err)
	}
	_, err = q.CreateChannelGroupRoute(ctx, sqlc.CreateChannelGroupRouteParams{
		ID: uuid.Must(uuid.NewV7()).String(), GroupMessageID: groupMessageID,
		GroupID: groupID, GroupSeq: message.Seq,
	})
	return err
}
