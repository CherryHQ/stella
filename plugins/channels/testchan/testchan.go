// Package testchan is a test-only channel adapter: it polls a fake platform's
// HTTP endpoint for inbound events and posts outbound operations back to it.
// Registration is gated on STELLA_TEST_CHANNELS — the adapter exists to let
// the durable multi-replica path run real process-boundary tests without real
// platform credentials.
package testchan

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

const (
	// PluginID is the managed plugin identity; the channel type is the same.
	PluginID    = "channel/testchan"
	RuntimeName = "testchan"
	// Platform is the IncomingMessage platform tag.
	Platform = "testchan"
)

// Config is the adapter's channel config.
type Config struct {
	InstanceID   string `json:"instance_id,omitempty"`
	Endpoint     string `json:"endpoint"`
	BotName      string `json:"bot_name,omitempty"`
	PollInterval int    `json:"poll_interval_ms,omitempty"`
}

// DecodeConfig parses the raw channel config.
func DecodeConfig(raw map[string]any) (Config, error) {
	data, err := json.Marshal(raw)
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func validateConfig(cfg Config) string {
	if cfg.Endpoint == "" {
		return "endpoint is required"
	}
	return ""
}

func configureConfig(cfg Config, desired pkgplugins.PluginState) Config {
	if cfg.InstanceID == "" {
		cfg.InstanceID = desired.ID
	}
	if cfg.BotName == "" {
		cfg.BotName = "testbot-" + desired.ID
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 250
	}
	return cfg
}

// pollEvent is one inbound event the fake platform returns.
type pollEvent struct {
	ID         string `json:"id"`
	ChatID     string `json:"chat_id"`
	ThreadID   string `json:"thread_id,omitempty"`
	ReplyTo    string `json:"reply_to,omitempty"`
	SenderID   string `json:"sender_id"`
	SenderName string `json:"sender_name,omitempty"`
	Text       string `json:"text"`
	Command    string `json:"command,omitempty"`
	Args       string `json:"args,omitempty"`
}

// sendFile is one staged attachment delivered with an operation.
type sendFile struct {
	Name string `json:"name"`
	Data string `json:"data"` // base64
}

// sendRequest is what the adapter posts for one outbound operation.
type sendRequest struct {
	Tag        string     `json:"tag,omitempty"`
	OpKey      string     `json:"op_key"`
	OpIndex    int        `json:"op_index"`
	ChatKey    string     `json:"chat_key"`
	ThreadKey  string     `json:"thread_key,omitempty"`
	ReplyToKey string     `json:"reply_to_key,omitempty"`
	Text       string     `json:"text"`
	Account    string     `json:"account"`
	Draft      bool       `json:"draft,omitempty"`
	MessageID  string     `json:"message_id,omitempty"`
	Files      []sendFile `json:"files,omitempty"`
}

// Channel is the test adapter.
type Channel struct {
	cfg     Config
	handler pkgchannel.Handler
	client  *http.Client
}

func newChannel(cfg Config, handler pkgchannel.Handler) (pkgchannel.Channel, error) {
	if msg := validateConfig(cfg); msg != "" {
		return nil, fmt.Errorf("testchan: %s", msg)
	}
	c := &Channel{cfg: cfg, handler: handler, client: &http.Client{Timeout: 30 * time.Second}}
	if registrar, ok := handler.(pkgchannel.BotRegistrar); ok {
		registrar.RegisterBotIdentity(Platform, cfg.BotName, cfg.InstanceID)
	}
	return c, nil
}

func (c *Channel) Name() string { return c.cfg.InstanceID }

func (c *Channel) Stop() {}

// Notify posts a notification through the same send endpoint.
func (c *Channel) Notify(ctx context.Context, n pkgchannel.Notification) error {
	req := sendRequest{ChatKey: n.ChatID, Text: n.Text, Account: c.cfg.BotName}
	return c.post(ctx, "/send", req)
}

func (c *Channel) post(ctx context.Context, path string, body any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.Endpoint+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("testchan send: status %d", resp.StatusCode)
	}
	return nil
}

// Start polls the fake platform until ctx ends.
func (c *Channel) Start(ctx context.Context) error {
	tick := time.Duration(c.cfg.PollInterval) * time.Millisecond
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(tick):
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.Endpoint+"/poll", nil)
		if err != nil {
			return err
		}
		resp, err := c.client.Do(req)
		if err != nil {
			continue // transient platform error — keep polling
		}
		var page struct {
			Events []pollEvent `json:"events"`
		}
		err = json.NewDecoder(resp.Body).Decode(&page)
		_ = resp.Body.Close()
		if err != nil {
			continue
		}
		for _, ev := range page.Events {
			c.deliver(ctx, ev)
		}
	}
}

func (c *Channel) deliver(ctx context.Context, ev pollEvent) {
	msg := pkgchannel.IncomingMessage{
		Platform:      Platform,
		ChannelID:     c.cfg.InstanceID,
		BotAccountKey: c.cfg.BotName,
		SenderID:      ev.SenderID,
		SenderName:    ev.SenderName,
		ChatID:        ev.ChatID,
		ThreadID:      ev.ThreadID,
		MessageID:     ev.ID,
		ReplyTo:       ev.ReplyTo,
		Content:       pkgchannel.TextContent(ev.Text),
	}
	resp, handled, stream, err := c.handler.HandleIncoming(ctx, msg, ev.Command, ev.Args)
	if err != nil {
		return
	}
	if handled {
		if resp != "" {
			_ = c.post(ctx, "/send", sendRequest{ChatKey: ev.ChatID, Text: resp, Account: c.cfg.BotName})
		}
		return
	}
	if stream == nil {
		return
	}
	// Legacy mode: drain the stream and post the final text.
	var text string
	for e := range stream.Events {
		text += e.Text
	}
	if text != "" {
		_ = c.post(ctx, "/send", sendRequest{ChatKey: ev.ChatID, Text: text, Account: c.cfg.BotName})
	}
}

// SendOperation implements pkgchannel.OperationSender.
func (c *Channel) SendOperation(ctx context.Context, op pkgchannel.OutboundOp) (pkgchannel.SendResult, error) {
	if op.Kind == "notify" {
		var payload struct {
			Notification pkgchannel.Notification `json:"notification"`
		}
		if err := json.Unmarshal(op.Payload, &payload); err != nil {
			return pkgchannel.SendResult{}, pkgchannel.SendErrorf(pkgchannel.SendPermanent, "testchan: bad notify payload: %v", err)
		}
		if err := c.Notify(ctx, payload.Notification); err != nil {
			return pkgchannel.SendResult{}, pkgchannel.SendErrorf(pkgchannel.SendUnknown, "testchan: notify send: %v", err)
		}
		return pkgchannel.SendResult{}, nil
	}
	var text string
	var files []sendFile
	switch op.Kind {
	case "send_text":
		var payload struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(op.Payload, &payload); err != nil {
			return pkgchannel.SendResult{}, pkgchannel.SendErrorf(pkgchannel.SendPermanent, "testchan: decode payload: %s", err)
		}
		text = payload.Text
	case "send_reply":
		var payload pkgchannel.ReplyOpPayload
		if err := json.Unmarshal(op.Payload, &payload); err != nil {
			return pkgchannel.SendResult{}, pkgchannel.SendErrorf(pkgchannel.SendPermanent, "testchan: decode send_reply payload: %s", err)
		}
		text = payload.Text
		if text == "" {
			text, _, _ = pkgchannel.CollectReplyEvents(payload.Events)
		}
	case "send_group_reply":
		var payload pkgchannel.GroupReplyOpPayload
		if err := json.Unmarshal(op.Payload, &payload); err != nil {
			return pkgchannel.SendResult{}, pkgchannel.SendErrorf(pkgchannel.SendPermanent, "testchan: decode send_group_reply payload: %s", err)
		}
		text = payload.Text
	case "send_attachment":
		var payload pkgchannel.AttachmentOpPayload
		if err := json.Unmarshal(op.Payload, &payload); err != nil {
			return pkgchannel.SendResult{}, pkgchannel.SendErrorf(pkgchannel.SendPermanent, "testchan: decode send_attachment payload: %s", err)
		}
		switch payload.Kind {
		case pkgchannel.AttachmentImage:
			files = append(files, sendFile{Name: "image." + imageExt(payload.MimeType), Data: payload.Data})
		case pkgchannel.AttachmentFile:
			data, err := pkgchannel.OpenAttachmentOp(ctx, c.handler, op)
			if err != nil {
				return pkgchannel.SendResult{}, pkgchannel.ClassifyAttachmentErr("testchan", err)
			}
			files = append(files, sendFile{Name: payload.Name, Data: base64.StdEncoding.EncodeToString(data)})
		default:
			return pkgchannel.SendResult{}, pkgchannel.SendErrorf(pkgchannel.SendPermanent, "testchan: unknown attachment kind %q", payload.Kind)
		}
	default:
		return pkgchannel.SendResult{}, pkgchannel.SendErrorf(pkgchannel.SendPermanent, "testchan: unsupported op kind %q", op.Kind)
	}
	req := sendRequest{
		Tag:        os.Getenv("STELLA_TESTCHAN_TAG"),
		OpKey:      op.DeliveryKey,
		OpIndex:    op.OperationIndex,
		ChatKey:    op.Address.ChatKey,
		ThreadKey:  op.Address.ThreadKey,
		ReplyToKey: op.Address.ReplyToKey,
		Text:       text,
		Account:    op.SourceAccountKey,
		MessageID:  op.DraftMessageID,
		Files:      files,
	}
	data, err := json.Marshal(req)
	if err != nil {
		return pkgchannel.SendResult{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.Endpoint+"/send", bytes.NewReader(data))
	if err != nil {
		return pkgchannel.SendResult{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(httpReq)
	if err != nil {
		// No response seen: the platform may have recorded it — unknown, never
		// a blind resend.
		return pkgchannel.SendResult{}, pkgchannel.SendErrorf(pkgchannel.SendUnknown, "testchan: %s", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == 429 || resp.StatusCode >= 500 {
		return pkgchannel.SendResult{}, pkgchannel.SendErrorf(pkgchannel.SendRetryable, "testchan: status %d", resp.StatusCode)
	}
	if resp.StatusCode >= 400 {
		return pkgchannel.SendResult{}, pkgchannel.SendErrorf(pkgchannel.SendPermanent, "testchan: status %d", resp.StatusCode)
	}
	var receipt struct {
		PlatformMessageID string `json:"platform_message_id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&receipt)
	return pkgchannel.SendResult{PlatformMessageID: receipt.PlatformMessageID}, nil
}

// SendDraftUpdate implements pkgchannel.DraftSender: the fake platform keeps
// one message per draft identity — first call creates it, later calls edit.
func (c *Channel) SendDraftUpdate(ctx context.Context, op pkgchannel.OutboundOp) (pkgchannel.SendResult, error) {
	var payload pkgchannel.DraftUpdatePayload
	if err := json.Unmarshal(op.Payload, &payload); err != nil {
		return pkgchannel.SendResult{}, pkgchannel.SendErrorf(pkgchannel.SendPermanent, "testchan: decode draft payload: %s", err)
	}
	req := sendRequest{
		Tag:        os.Getenv("STELLA_TESTCHAN_TAG"),
		OpKey:      op.DeliveryKey,
		OpIndex:    op.OperationIndex,
		ChatKey:    op.Address.ChatKey,
		ThreadKey:  op.Address.ThreadKey,
		ReplyToKey: op.Address.ReplyToKey,
		Text:       payload.Text,
		Account:    op.SourceAccountKey,
		Draft:      true,
		MessageID:  op.DraftMessageID,
	}
	data, err := json.Marshal(req)
	if err != nil {
		return pkgchannel.SendResult{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.Endpoint+"/send", bytes.NewReader(data))
	if err != nil {
		return pkgchannel.SendResult{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(httpReq)
	if err != nil {
		return pkgchannel.SendResult{}, pkgchannel.SendErrorf(pkgchannel.SendUnknown, "testchan: %s", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == 429 || resp.StatusCode >= 500 {
		return pkgchannel.SendResult{}, pkgchannel.SendErrorf(pkgchannel.SendRetryable, "testchan: status %d", resp.StatusCode)
	}
	if resp.StatusCode >= 400 {
		return pkgchannel.SendResult{}, pkgchannel.SendErrorf(pkgchannel.SendPermanent, "testchan: status %d", resp.StatusCode)
	}
	var receipt struct {
		PlatformMessageID string `json:"platform_message_id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&receipt)
	return pkgchannel.SendResult{PlatformMessageID: receipt.PlatformMessageID}, nil
}

func imageExt(mime string) string {
	if _, after, ok := strings.Cut(mime, "/"); ok {
		return after
	}
	return "bin"
}

// OwnsAccount implements pkgchannel.AccountChecker.
func (c *Channel) OwnsAccount(accountKey string) bool {
	return accountKey == c.cfg.BotName
}
