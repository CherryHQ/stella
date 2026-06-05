package email

import (
	"errors"
	"time"
)

var ErrMessageNotFound = errors.New("message not found")

// Envelope holds metadata about an email message.
type Envelope struct {
	UID            uint32    `json:"uid"`
	MessageID      string    `json:"message_id,omitempty"`
	From           string    `json:"from"`
	FromName       string    `json:"from_name,omitempty"`
	FromAddr       string    `json:"from_addr,omitempty"`
	To             []string  `json:"to"`
	Subject        string    `json:"subject"`
	Date           time.Time `json:"date"`
	Flags          []string  `json:"flags"`
	HasAttachments bool      `json:"has_attachments"`
	Size           int64     `json:"size"`
}

// AttachmentInfo holds metadata about a message attachment (no content).
type AttachmentInfo struct {
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
	MIMEType string `json:"mime_type"`
}

// Message is a fully-fetched email message including body and attachment metadata.
type Message struct {
	Envelope
	TextBody    string           `json:"text_body,omitempty"`
	HTMLBody    string           `json:"html_body,omitempty"`
	Attachments []AttachmentInfo `json:"attachments,omitempty"`
}

// ListOptions controls which messages are returned by List.
type ListOptions struct {
	Folder  string
	Limit   int
	Unread  bool
	From    string
	Subject string
	Since   *time.Time
	Before  *time.Time
}
