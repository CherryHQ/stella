package outbox

import (
	"encoding/json"

	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

// AddressVersion / PayloadVersion stamp the frozen JSONB value objects. Bump
// on shape change; senders must keep reading older versions.
const (
	AddressVersion = 1
	PayloadVersion = 1
)

// Address fixes where an operation lands: physical chat coordinates plus the
// reply anchor. The platform sender replays it verbatim — it never re-resolves.
type Address struct {
	V          int    `json:"v"`
	ChatKey    string `json:"chat_key"`
	ThreadKey  string `json:"thread_key,omitempty"`
	ReplyToKey string `json:"reply_to_key,omitempty"`
}

// TextPayload is the frozen body of a send_text op.
type TextPayload struct {
	V    int    `json:"v"`
	Text string `json:"text"`
}

// ReplyOps builds the standard "one text reply" delivery for an inbound event.
// deliveryKey must stay stable across retries so a re-appended completion can
// never double-send.
func ReplyOps(runID, deliveryKey, channelID, accountKey string, addr Address, text string) ([]Op, error) {
	a, err := json.Marshal(addr)
	if err != nil {
		return nil, err
	}
	p, err := json.Marshal(TextPayload{V: PayloadVersion, Text: text})
	if err != nil {
		return nil, err
	}
	return []Op{{
		RunID:       runID,
		DeliveryKey: deliveryKey,
		Index:       0,
		Kind:        OpSendText,
		ChannelID:   channelID,
		AccountKey:  accountKey,
		Address:     a,
		Payload:     p,
	}}, nil
}

// CommandReplyKey names the delivery that carries a command's immediate reply.
// One command event produces exactly one reply op set.
func CommandReplyKey(inboxID string) string { return "reply:" + inboxID }

// DeliveryKeyForRun names the final-reply delivery of a run.
func DeliveryKeyForRun(runID string) string { return "run:" + runID }

// ChatKeyFor derives the physical chat coordinate the same way
// messageDeliveryCoordinates does: platform chat id, falling back to the
// sender id for DMs on platforms that leave ChatID empty.
func ChatKeyFor(msg pkgchannel.IncomingMessage) string {
	if msg.ChatID != "" {
		return msg.ChatID
	}
	return msg.SenderID
}
