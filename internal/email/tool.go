package email

import (
	"context"
	"fmt"
	"time"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/tools/toolruntime"
	"github.com/CherryHQ/stella/pkg/tools"
)

const (
	defaultToolPageSize = 20
	maxToolPageSize     = 100
)

type Tool struct{ svc *Service }

func NewTool(svc *Service) *Tool { return &Tool{svc: svc} }
func (t *Tool) Definition() tools.Definition {
	return tools.Definition{Name: ToolName, Description: "Read configured email accounts, list/read messages, and send mail for this user. Actions: accounts, list, read, send. Send requires idempotency_key; reuse the same key only when retrying the exact same send. Message bodies are truncated for token safety. Never exposes passwords or EMAIL_CONFIG contents.", InputSchema: InputSchema()}
}

func (t *Tool) Execute(ctx context.Context, args map[string]any) (string, error) {
	if t == nil || t.svc == nil {
		return "", fmt.Errorf("email service is unavailable — try again later")
	}
	ident, err := toolruntime.ToolIdentity(ctx, "email")
	if err != nil {
		return "", err
	}
	action, err := toolruntime.ActionArg(args, "email")
	if err != nil {
		return "", err
	}
	out, err := Dispatch(ctx, emailHandler{svc: t.svc, ident: ident}, action, args)
	if err != nil {
		return "", toolruntime.MapError("email", err)
	}
	return toolruntime.MarshalResult(out)
}

type emailHandler struct {
	svc   *Service
	ident authz.Identity
}

func (h emailHandler) Accounts(ctx context.Context, _ AccountsInput) (any, error) {
	accounts, err := h.svc.As(h.ident).Accounts(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]any{"accounts": accounts.Accounts, "default": accounts.Default}, nil
}

func (h emailHandler) List(ctx context.Context, in ListInput) (any, error) {
	limit := in.Limit
	if limit == 0 {
		limit = defaultToolPageSize
	}
	if limit < 1 || limit > maxToolPageSize {
		return nil, fmt.Errorf("invalid limit — use a value between 1 and %d", maxToolPageSize)
	}
	opts := ListOptions{Limit: limit, Folder: in.Folder, From: in.From, Subject: in.Subject}
	if in.Unread != nil {
		opts.Unread = *in.Unread
	}
	if in.Since != "" {
		if t, err := time.Parse("2006-01-02", in.Since); err == nil {
			opts.Since = &t
		}
	}
	if in.Before != "" {
		if t, err := time.Parse("2006-01-02", in.Before); err == nil {
			opts.Before = &t
		}
	}
	msgs, err := h.svc.As(h.ident).List(ctx, in.Account, opts)
	if err != nil {
		return nil, err
	}
	items := make([]emailEnvelopeResponse, 0, len(msgs))
	for _, msg := range msgs {
		items = append(items, emailEnvelopeSummary(msg))
	}
	return listResponse[emailEnvelopeResponse]{Items: items, HasMore: false}, nil
}

func (h emailHandler) Read(ctx context.Context, in ReadInput) (any, error) {
	msg, err := h.svc.As(h.ident).Read(ctx, in.Account, in.Folder, uint32(in.Uid))
	if err != nil {
		return nil, err
	}
	return emailMessageSummary(msg), nil
}

func (h emailHandler) Send(ctx context.Context, in SendInput) (any, error) {
	opts := SendOptions{To: stringItems(in.To), Cc: stringItems(in.Cc), Bcc: stringItems(in.Bcc), Subject: in.Subject, Body: in.Body, From: in.From, ReplyTo: in.ReplyTo, InReplyTo: in.InReplyTo}
	if in.Html != nil {
		opts.HTML = *in.Html
	}
	result, err := h.svc.As(h.ident).Send(ctx, in.Account, opts, in.IdempotencyKey)
	if err != nil {
		return nil, err
	}
	if result.Duplicate {
		return map[string]any{"status": result.Status, "duplicate_suppressed": true}, nil
	}
	return map[string]any{"status": result.Status}, nil
}

type emailEnvelopeResponse struct {
	UID         uint32 `json:"uid"`
	From        string `json:"from"`
	Subject     string `json:"subject"`
	Date        string `json:"date"`
	Snippet     string `json:"snippet,omitempty"`
	Attachments bool   `json:"has_attachments,omitempty"`
}
type emailMessageResponse struct {
	Envelope  emailEnvelopeResponse `json:"envelope"`
	Body      string                `json:"body"`
	Truncated bool                  `json:"truncated"`
	Note      string                `json:"note,omitempty"`
}
type listResponse[T any] struct {
	Items         []T    `json:"items"`
	HasMore       bool   `json:"has_more"`
	NextPageToken string `json:"next_page_token,omitempty"`
}

func emailEnvelopeSummary(msg Envelope) emailEnvelopeResponse {
	return emailEnvelopeResponse{UID: msg.UID, From: msg.From, Subject: msg.Subject, Date: msg.Date.UTC().Format(time.RFC3339), Attachments: msg.HasAttachments}
}

func emailMessageSummary(msg *Message) emailMessageResponse {
	body := msg.TextBody
	if body == "" {
		body = msg.HTMLBody
	}
	body, truncated := tools.TruncateText(body, 50*1024)
	out := emailMessageResponse{Envelope: emailEnvelopeSummary(msg.Envelope), Body: body, Truncated: truncated}
	if truncated {
		out.Note = "truncated — use the web UI or email client for the full message"
	}
	return out
}

func stringItems(items []any) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
