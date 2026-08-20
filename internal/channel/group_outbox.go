package channel

import (
	"encoding/json"

	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
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
