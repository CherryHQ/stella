// Package email contains the transport-neutral contract for the user-owned
// email capability. The implementation lives in plugins/email.
package email

import (
	"context"
	"errors"
	"time"
)

const ConfigName = "EMAIL_CONFIG"

var ErrMessageNotFound = errors.New("message not found")

// Access is one email use case bound to the acting user resolved by the
// implementation's trusted context adapter. It never accepts a user ID.
type Access interface {
	Accounts(context.Context) (AccountList, error)
	Folders(context.Context, string) ([]string, error)
	List(context.Context, string, ListOptions) ([]Envelope, error)
	Read(context.Context, string, string, uint32) (*Message, error)
	MarkSeen(context.Context, string, string, uint32, bool) error
	Send(context.Context, string, SendOptions, string) (SendResult, error)
}

// Service resolves the trusted caller from context and captures it in Access.
type Service interface {
	Access(context.Context) (Access, error)
}

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

type AttachmentInfo struct {
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
	MIMEType string `json:"mime_type"`
}

type Message struct {
	Envelope
	TextBody    string           `json:"text_body,omitempty"`
	HTMLBody    string           `json:"html_body,omitempty"`
	Attachments []AttachmentInfo `json:"attachments,omitempty"`
}

type ListOptions struct {
	Folder  string
	Limit   int
	Unread  bool
	From    string
	Subject string
	Since   *time.Time
	Before  *time.Time
}

// SendOptions holds the parameters for composing and sending an email.
type SendOptions struct {
	To          []string
	Cc          []string
	Bcc         []string
	Subject     string
	Body        string
	HTML        bool
	Attachments []string // file paths
	From        string   // override sender
	ReplyTo     string
	InReplyTo   string // Message-ID of the message being replied to
}

type SendResult struct {
	Status    string
	Duplicate bool
}

type AccountList struct {
	Accounts []string
	Default  string
}
