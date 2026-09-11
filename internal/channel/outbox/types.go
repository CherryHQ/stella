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
	// Token carries the platform reply credential when one is required —
	// Weixin's context_token authorizes a reply to a conversation.
	Token string `json:"token,omitempty"`
	// Scope marks the platform conversation kind when the chat key alone is
	// ambiguous — QQ needs "group"/"c2c" to pick the API endpoint.
	Scope string `json:"scope,omitempty"`
}

// TextPayload is the frozen body of a send_text op.
type TextPayload struct {
	V    int    `json:"v"`
	Text string `json:"text"`
}

// ReplyOps builds the standard text reply delivery for an inbound event.
// deliveryKey must stay stable across retries so a re-appended completion can
// never double-send. When maxLen > 0 the text is split into one op per chunk,
// each chained on its predecessor so a retried middle chunk never lets a
// later chunk overtake it.
func ReplyOps(runID, deliveryKey, channelID, accountKey string, addr Address, text string, maxLen int) ([]Op, error) {
	a, err := json.Marshal(addr)
	if err != nil {
		return nil, err
	}
	chunks := []string{text}
	if maxLen > 0 {
		chunks = pkgchannel.SplitMessage(text, maxLen)
	}
	ops := make([]Op, 0, len(chunks))
	for i, chunk := range chunks {
		p, err := json.Marshal(TextPayload{V: PayloadVersion, Text: chunk})
		if err != nil {
			return nil, err
		}
		op := Op{
			RunID:       runID,
			DeliveryKey: deliveryKey,
			Index:       i,
			Kind:        OpSendText,
			ChannelID:   channelID,
			AccountKey:  accountKey,
			Address:     a,
			Payload:     p,
		}
		if i > 0 {
			op.DependsOn = []int{i - 1}
		}
		ops = append(ops, op)
	}
	return ops, nil
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
