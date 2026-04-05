package runner

import (
	"encoding/json"
	"fmt"

	"github.com/vaayne/anna/pkg/ai"
)

// Envelope wraps stream events for transport.
type Envelope struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// EncodeEvent serializes normalized assistant events.
func EncodeEvent(event ai.AssistantEvent) ([]byte, error) {
	payload, err := json.Marshal(event)
	if err != nil {
		return nil, err
	}
	env := Envelope{Type: eventName(event), Data: payload}
	return json.Marshal(env)
}

// DecodeEvent deserializes envelope to concrete event type.
func DecodeEvent(raw []byte) (ai.AssistantEvent, error) {
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, err
	}

	switch env.Type {
	case "textDelta":
		var e ai.EventTextDelta
		if err := json.Unmarshal(env.Data, &e); err != nil {
			return nil, err
		}
		return e, nil
	case "stop":
		var e ai.EventStop
		if err := json.Unmarshal(env.Data, &e); err != nil {
			return nil, err
		}
		return e, nil
	case "usage":
		var e ai.EventUsage
		if err := json.Unmarshal(env.Data, &e); err != nil {
			return nil, err
		}
		return e, nil
	default:
		return nil, fmt.Errorf("unsupported event type %q", env.Type)
	}
}

func eventName(event ai.AssistantEvent) string {
	switch event.(type) {
	case ai.EventTextDelta:
		return "textDelta"
	case ai.EventStop:
		return "stop"
	case ai.EventUsage:
		return "usage"
	default:
		return "unknown"
	}
}
