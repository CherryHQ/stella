package channel

import (
	"encoding/json"

	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

// GroupOutboxEnvelope stores dispatch metadata that must be decided at ingest
// time, while the group_id and membership view are available in the append
// transaction.
type GroupOutboxEnvelope struct {
	Mentions []pkgchannel.Mention `json:"mentions,omitempty"`
}

func EncodeGroupOutboxEnvelope(mentions []pkgchannel.Mention) (string, error) {
	data, err := json.Marshal(GroupOutboxEnvelope{Mentions: mentions})
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
