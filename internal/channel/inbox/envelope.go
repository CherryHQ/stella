package inbox

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/CherryHQ/stella/pkg/ai"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

// EnvelopeVersion is the payload_version stamped on channel_inbox rows. Bump
// on any shape change; Decode must keep reading older versions.
const EnvelopeVersion = 1

// Attachment is one inbound media item. Handle carries the platform's stable
// re-fetch reference (file id, upload ticket); LocalRef is set once staging
// has written the durable media record. An event with any attachment lacking
// LocalRef must stay 'received' until a stager promotes or fails it.
type Attachment struct {
	Kind     string          `json:"kind"` // image / file / audio / video
	Name     string          `json:"name,omitempty"`
	MimeType string          `json:"mime_type,omitempty"`
	Handle   json.RawMessage `json:"handle,omitempty"`
	LocalRef string          `json:"local_ref,omitempty"`
}

// Envelope is the versioned payload a channel_inbox row carries: the
// normalized event plus the platform coordinates a reply needs. Everything
// the router requires lives here — it never re-reads the platform.
type Envelope struct {
	V           int             `json:"v"`
	Platform    string          `json:"platform"`
	SenderID    string          `json:"sender_id,omitempty"`
	SenderIDs   []string        `json:"sender_ids,omitempty"`
	SenderName  string          `json:"sender_name,omitempty"`
	ChatID      string          `json:"chat_id,omitempty"`
	IsGroup     bool            `json:"is_group,omitempty"`
	ThreadID    string          `json:"thread_id,omitempty"`
	MessageID   string          `json:"message_id,omitempty"`
	ReplyTo     string          `json:"reply_to,omitempty"`
	Command     string          `json:"command,omitempty"`
	Args        string          `json:"args,omitempty"`
	Content     json.RawMessage `json:"content,omitempty"` // ai.MarshalContentBlocks output
	Attachments []Attachment    `json:"attachments,omitempty"`
	Timestamp   time.Time       `json:"timestamp,omitempty"`
}

// MarshalIncoming converts a live IncomingMessage into the storable envelope.
// Content blocks are serialized with the canonical reference encoding —
// provider-ready byte payloads are rejected because a durable record cannot
// hold what it cannot re-read.
func MarshalIncoming(msg pkgchannel.IncomingMessage, command, args string) (json.RawMessage, error) {
	env := Envelope{
		V:          EnvelopeVersion,
		Platform:   msg.Platform,
		SenderID:   msg.SenderID,
		SenderIDs:  msg.SenderIDs,
		SenderName: msg.SenderName,
		ChatID:     msg.ChatID,
		IsGroup:    msg.IsGroup,
		ThreadID:   msg.ThreadID,
		MessageID:  msg.MessageID,
		ReplyTo:    msg.ReplyTo,
		Command:    command,
		Args:       args,
		Timestamp:  msg.Timestamp.UTC(),
	}
	if len(msg.Content) > 0 {
		if err := ai.ValidateCanonicalContentBlocks(msg.Content); err != nil {
			return nil, fmt.Errorf("inbox: non-persistable content: %w", err)
		}
		blocks, err := ai.MarshalContentBlocks(msg.Content)
		if err != nil {
			return nil, fmt.Errorf("inbox: marshal content: %w", err)
		}
		env.Content = blocks
	}
	return json.Marshal(env)
}

// Decode reads a stored envelope. Unknown versions are rejected loudly —
// guessing at a foreign shape is how phantom runs get created.
func Decode(payload json.RawMessage) (*Envelope, error) {
	var env Envelope
	if err := json.Unmarshal(payload, &env); err != nil {
		return nil, fmt.Errorf("inbox: decode envelope: %w", err)
	}
	if env.V != EnvelopeVersion {
		return nil, fmt.Errorf("inbox: envelope version %d, want %d", env.V, EnvelopeVersion)
	}
	return &env, nil
}

// ContentBlocks decodes the stored content for the run input.
func (e *Envelope) ContentBlocks() ([]ai.ContentBlock, error) {
	if len(e.Content) == 0 {
		return nil, nil
	}
	return ai.UnmarshalContentBlocks(e.Content)
}

// NeedsStaging reports whether any attachment still waits for a durable
// local reference. Such events route as 'received' until staged.
func (e *Envelope) NeedsStaging() bool {
	for _, a := range e.Attachments {
		if a.LocalRef == "" {
			return true
		}
	}
	return false
}
