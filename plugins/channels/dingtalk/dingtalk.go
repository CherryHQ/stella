package dingtalk

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/open-dingtalk/dingtalk-stream-sdk-go/chatbot"
	streamclient "github.com/open-dingtalk/dingtalk-stream-sdk-go/client"

	internalchannel "github.com/CherryHQ/stella/internal/channel"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/channel"
)

const (
	dingTalkMaxMessageLen = 18000
	seenMessageTTL        = 5 * time.Minute
	dingTalkTurnTimeout   = 15 * time.Minute
)

type Config struct {
	InstanceID     string
	ClientID       string
	ClientSecret   string
	AllowGroup     bool
	AllowDM        bool
	RequireMention bool
}

type streamClient interface {
	RegisterChatBotCallbackRouter(chatbot.IChatBotMessageHandler)
	Start(context.Context) error
	Close()
}

type dingTalkStreamClient struct {
	*streamclient.StreamClient
}

func (c *dingTalkStreamClient) Close() {
	// The SDK's reconnect loop ignores the Start context. Disable it before
	// closing so a reconciled or stopped plugin cannot resurrect its connection.
	c.AutoReconnect = false
	c.StreamClient.Close()
}

type webhookSession struct {
	URL       string
	ExpiresAt time.Time
}

type Bot struct {
	handler channel.Handler
	cfg     Config

	mu             sync.RWMutex
	provisionMu    sync.Mutex
	closeOnce      sync.Once
	ctx            context.Context
	cancel         context.CancelFunc
	client         streamClient
	dmSessions     map[string]webhookSession
	groupSessions  map[string]webhookSession
	seenMessages   map[string]time.Time
	registeredBots map[string]struct{}
	finalized      bool
	provisioned    map[string]struct{}
	clientFactory  func() streamClient
	replyToWebhook func(context.Context, string, string) error
}

func logger() *slog.Logger { return slog.With("component", "dingtalk") }

func New(cfg Config, handler channel.Handler) (*Bot, error) {
	if strings.TrimSpace(cfg.ClientID) == "" || strings.TrimSpace(cfg.ClientSecret) == "" {
		return nil, fmt.Errorf("dingtalk: client_id and client_secret are required")
	}
	b := &Bot{
		handler:        handler,
		cfg:            cfg,
		dmSessions:     make(map[string]webhookSession),
		groupSessions:  make(map[string]webhookSession),
		seenMessages:   make(map[string]time.Time),
		registeredBots: make(map[string]struct{}),
		provisioned:    make(map[string]struct{}),
	}
	b.clientFactory = func() streamClient {
		return &dingTalkStreamClient{StreamClient: streamclient.NewStreamClient(streamclient.WithAppCredential(
			streamclient.NewAppCredentialConfig(cfg.ClientID, cfg.ClientSecret),
		))}
	}
	b.replyToWebhook = sendWebhookText
	if registrar, ok := handler.(interface {
		RegisterGroupPublisher(string, internalchannel.GroupPublisher)
	}); ok {
		registrar.RegisterGroupPublisher(b.Name(), b)
	}
	return b, nil
}

func (b *Bot) Name() string {
	if b.cfg.InstanceID != "" {
		return b.cfg.InstanceID
	}
	return channel.PlatformDingTalk
}

func (b *Bot) Platform() string { return channel.PlatformDingTalk }

func (b *Bot) Start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b.mu.Lock()
	if b.finalized {
		b.mu.Unlock()
		return fmt.Errorf("dingtalk: finalized during startup")
	}
	b.ctx, b.cancel = context.WithCancel(ctx)
	client := b.clientFactory()
	b.client = client
	client.RegisterChatBotCallbackRouter(b.onMessage)
	b.mu.Unlock()

	if err := client.Start(b.ctx); err != nil {
		b.closeClient()
		return fmt.Errorf("dingtalk: start stream client: %w", err)
	}
	logger().Info("dingtalk bot started (Stream mode)")
	<-b.ctx.Done()
	b.closeClient()
	return b.ctx.Err()
}

func (b *Bot) Stop() {
	b.mu.RLock()
	cancel := b.cancel
	client := b.client
	b.mu.RUnlock()
	if cancel != nil {
		cancel()
	}
	if client != nil {
		b.closeClient()
	}
}

func (b *Bot) closeClient() {
	b.closeOnce.Do(func() {
		b.mu.RLock()
		client := b.client
		b.mu.RUnlock()
		if client != nil {
			client.Close()
		}
	})
}

func (b *Bot) Finalize() {
	b.mu.Lock()
	if b.finalized {
		b.mu.Unlock()
		return
	}
	b.finalized = true
	botIDs := make([]string, 0, len(b.registeredBots))
	for botID := range b.registeredBots {
		botIDs = append(botIDs, botID)
	}
	clear(b.registeredBots)
	b.mu.Unlock()
	for _, botID := range botIDs {
		if registrar, ok := b.handler.(interface {
			UnregisterBotIdentity(string, string, string)
		}); ok {
			registrar.UnregisterBotIdentity(channel.PlatformDingTalk, botID, b.Name())
		}
	}
	if registrar, ok := b.handler.(interface{ UnregisterGroupPublisher(string) }); ok {
		registrar.UnregisterGroupPublisher(b.Name())
	}
}

func (b *Bot) onMessage(callbackCtx context.Context, data *chatbot.BotCallbackDataModel) ([]byte, error) {
	if b.pollStopped() {
		return nil, nil
	}
	if data == nil || b.wasSeen(data.MsgId) {
		return nil, nil
	}
	// The pinned DingTalk stream SDK exposes only text bodies. Reject every
	// advertised non-text msgtype instead of acknowledging an attachment whose
	// expiring bytes the SDK cannot retrieve and make immutable.
	if data.Msgtype != "" && data.Msgtype != "text" {
		return nil, fmt.Errorf("dingtalk: unsupported inbound message type %q", data.Msgtype)
	}
	if strings.TrimSpace(data.Text.Content) == "" {
		return nil, fmt.Errorf("dingtalk: text callback has no content")
	}
	if data.SenderCorpId == "" || data.ChatbotCorpId == "" || data.SenderCorpId != data.ChatbotCorpId {
		logger().Warn("ignoring DingTalk callback outside the bot's enterprise", "sender_corp_id", data.SenderCorpId)
		return nil, nil
	}
	var isGroup bool
	switch data.ConversationType {
	case "1":
		isGroup = false
	case "2":
		isGroup = true
	default:
		logger().Warn("ignoring DingTalk callback with unknown conversation type", "conversation_type", data.ConversationType)
		return nil, nil
	}
	if !b.admit(data, isGroup) {
		return nil, nil
	}
	senderIDs := orderedIDs(data.SenderStaffId, data.SenderId)
	if len(senderIDs) == 0 || data.ConversationId == "" || data.SessionWebhook == "" {
		return nil, nil
	}
	b.rememberSession(data, senderIDs, isGroup)
	b.registerBotIdentity(data.ChatbotUserId)

	msg := channel.IncomingMessage{
		Platform:   channel.PlatformDingTalk,
		ChannelID:  b.Name(),
		SenderID:   senderIDs[0],
		SenderIDs:  senderIDs,
		SenderName: data.SenderNick,
		ChatID:     data.ConversationId,
		IsGroup:    isGroup,
		MessageID:  data.MsgId,
		Timestamp:  dingTalkEventTime(data.CreateAt),
		Content:    channel.TextContent(strings.TrimSpace(data.Text.Content)),
		Mentions:   dingTalkMentions(data.AtUsers),
	}
	if isGroup {
		msg.ReplyCapability = &channel.ReplyCapability{
			Kind: "dingtalk_session_webhook", Secret: data.SessionWebhook,
			ExpiresAt: dingTalkWebhookExpiry(data.SessionWebhookExpiredTime),
		}
	}
	parent := callbackCtx
	b.mu.RLock()
	if b.ctx != nil {
		parent = b.ctx
	}
	b.mu.RUnlock()
	ctx, cancel := context.WithTimeout(parent, dingTalkTurnTimeout)
	if msg.IsGroup {
		if err := b.ensureGroupMember(ctx, msg.ChatID); err != nil {
			cancel()
			return nil, fmt.Errorf("ensure group member: %w", err)
		}
	}
	plain := ai.FlattenText(msg.Content)
	cmd, args := channel.ParseSlashCommand(plain)
	resp, handled, stream, err := b.handler.HandleIncoming(ctx, msg, cmd, args)
	if err != nil {
		cancel()
		// Returning the callback error prevents DingTalk from observing an
		// acknowledgement before Stella durably accepts the stable delivery.
		return nil, fmt.Errorf("accept incoming message: %w", err)
	}
	b.markSeen(data.MsgId)
	go b.finishIncoming(ctx, cancel, data.SessionWebhook, resp, handled, stream)
	return nil, nil
}

func (b *Bot) admit(data *chatbot.BotCallbackDataModel, isGroup bool) bool {
	if !isGroup {
		if !b.cfg.AllowDM {
			logger().Debug("ignoring direct message because DMs are disabled")
			return false
		}
		return true
	}
	if !b.cfg.AllowGroup {
		logger().Debug("ignoring group message because group chats are disabled", "conversation_id", data.ConversationId)
		return false
	}
	if b.cfg.RequireMention && !data.IsInAtList {
		logger().Debug("ignoring group message without bot mention", "conversation_id", data.ConversationId)
		return false
	}
	return true
}

func (b *Bot) finishIncoming(ctx context.Context, cancel context.CancelFunc, webhook, resp string, handled bool, stream *channel.ChatStream) {
	defer cancel()
	if handled {
		_ = b.reply(ctx, webhook, resp)
		return
	}
	if stream == nil {
		return
	}
	response, streamErr := collectStream(ctx, stream)
	if streamErr != nil {
		if response != "" {
			response += "\n\n"
		}
		response += "Agent error: " + streamErr.Error()
	}
	if strings.TrimSpace(response) == "" {
		response = "(empty response)"
	}
	if err := b.replyStream(ctx, stream, webhook, response); err != nil {
		logger().Error("reply failed", "error", err)
	}
}

func (b *Bot) ensureGroupMember(ctx context.Context, groupID string) error {
	provisioner, ok := b.handler.(interface {
		EnsurePlatformGroupMember(context.Context, string, string, string) error
	})
	if !ok {
		return nil
	}
	b.provisionMu.Lock()
	defer b.provisionMu.Unlock()
	b.mu.RLock()
	if _, ok := b.provisioned[groupID]; ok {
		b.mu.RUnlock()
		return nil
	}
	b.mu.RUnlock()
	if err := provisioner.EnsurePlatformGroupMember(ctx, channel.PlatformDingTalk, groupID, b.Name()); err != nil {
		return err
	}
	b.mu.Lock()
	b.provisioned[groupID] = struct{}{}
	b.mu.Unlock()
	return nil
}

func (b *Bot) Publish(ctx context.Context, req internalchannel.GroupPublishRequest) error {
	defer req.Stream.Discard()
	stream, err := internalchannel.ValidateGroupReplay(ctx, req.Stream)
	if err != nil {
		return err
	}
	if stream == nil {
		return nil
	}
	response, streamErr := collectStream(ctx, stream)
	if streamErr != nil {
		return fmt.Errorf("dingtalk: render group replay: %w", streamErr)
	}
	if strings.TrimSpace(response) == "" {
		response = "(empty response)"
	}
	session, ok := b.groupSessionFor(req.PlatformGroupID)
	if !ok {
		return fmt.Errorf("dingtalk: no active session webhook for group %q", req.PlatformGroupID)
	}
	return b.replyStream(ctx, req.Stream, session.URL, response)
}

type durableGroupPublisher struct {
	webhook string
	expires time.Time
}

// NewDurableGroupPublisher reconstructs egress from an encrypted capability
// resolved by the host. It intentionally owns no listener or process-local
// session map.
func NewDurableGroupPublisher(webhook string, expires time.Time) (internalchannel.GroupPublisher, error) {
	if !expires.After(time.Now().UTC()) {
		return nil, fmt.Errorf("dingtalk: reply capability expired")
	}
	u, err := url.Parse(webhook)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return nil, fmt.Errorf("dingtalk: invalid reply capability")
	}
	return durableGroupPublisher{webhook: webhook, expires: expires.UTC()}, nil
}

func (p durableGroupPublisher) Publish(ctx context.Context, req internalchannel.GroupPublishRequest) error {
	defer req.Stream.Discard()
	if !time.Now().UTC().Before(p.expires) {
		return fmt.Errorf("dingtalk: reply capability expired")
	}
	response, streamErr := collectStream(ctx, req.Stream)
	if streamErr != nil {
		if response != "" {
			response += "\n\n"
		}
		response += "Agent error: " + streamErr.Error()
	}
	if strings.TrimSpace(response) == "" {
		response = "(empty response)"
	}
	for i, chunk := range channel.SplitMessage(response, dingTalkMaxMessageLen) {
		if err := req.Stream.CheckOperation(ctx); err != nil {
			return err
		}
		if err := sendWebhookText(ctx, p.webhook, chunk); err != nil {
			return fmt.Errorf("dingtalk: send reply chunk %d: %w", i+1, err)
		}
	}
	return nil
}

func (b *Bot) Notify(ctx context.Context, n channel.Notification) error {
	target := strings.TrimPrefix(n.ChatID, "dingtalk:")
	if target == "" {
		target = strings.TrimPrefix(n.RecipientID, "dingtalk:")
	}
	session, ok := b.dmSessionFor(target)
	if !ok {
		return fmt.Errorf("dingtalk: no active session webhook for %q; ask the user to message the bot again", target)
	}
	return b.reply(ctx, session.URL, n.Text)
}

func (b *Bot) reply(ctx context.Context, webhook, text string) error {
	chunks := channel.SplitMessage(text, dingTalkMaxMessageLen)
	for i, chunk := range chunks {
		if err := b.replyToWebhook(ctx, webhook, chunk); err != nil {
			return fmt.Errorf("dingtalk: send reply chunk %d/%d: %w", i+1, len(chunks), err)
		}
	}
	return nil
}

func (b *Bot) replyStream(ctx context.Context, stream *channel.ChatStream, webhook, text string) error {
	defer stream.Discard()
	chunks := channel.SplitMessage(text, dingTalkMaxMessageLen)
	for i, chunk := range chunks {
		if err := stream.CheckOperation(ctx); err != nil {
			return err
		}
		if err := b.replyToWebhook(ctx, webhook, chunk); err != nil {
			return fmt.Errorf("dingtalk: send reply chunk %d/%d: %w", i+1, len(chunks), err)
		}
	}
	return nil
}

func sendWebhookText(ctx context.Context, webhook, text string) error {
	u, err := url.Parse(webhook)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("invalid session webhook")
	}
	body, err := json.Marshal(map[string]any{"msgtype": "text", "text": map[string]string{"content": text}})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhook, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	data, readErr := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if readErr != nil {
		return fmt.Errorf("read webhook response: %w", readErr)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("webhook returned %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	var result struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return fmt.Errorf("decode webhook response: %w", err)
	}
	if result.ErrCode != 0 {
		return fmt.Errorf("webhook rejected message: errcode=%d errmsg=%s", result.ErrCode, result.ErrMsg)
	}
	return nil
}

func (b *Bot) rememberSession(data *chatbot.BotCallbackDataModel, senderIDs []string, isGroup bool) {
	expiresAt := dingTalkWebhookExpiry(data.SessionWebhookExpiredTime)
	session := webhookSession{URL: data.SessionWebhook, ExpiresAt: expiresAt}
	b.mu.Lock()
	b.pruneSessionsLocked(time.Now().UTC())
	if isGroup {
		b.groupSessions[data.ConversationId] = session
	} else {
		b.dmSessions[data.ConversationId] = session
		for _, id := range senderIDs {
			b.dmSessions[id] = session
		}
	}
	b.mu.Unlock()
}

func dingTalkWebhookExpiry(milliseconds int64) time.Time {
	if milliseconds > 0 {
		return time.UnixMilli(milliseconds).UTC()
	}
	return time.Now().UTC().Add(time.Hour)
}

func (b *Bot) dmSessionFor(target string) (webhookSession, bool) {
	target = strings.TrimPrefix(target, "dingtalk:")
	b.mu.RLock()
	session, ok := b.dmSessions[target]
	b.mu.RUnlock()
	return session, ok && session.URL != "" && time.Now().UTC().Before(session.ExpiresAt)
}

func (b *Bot) groupSessionFor(target string) (webhookSession, bool) {
	target = strings.TrimPrefix(target, "dingtalk:")
	b.mu.RLock()
	session, ok := b.groupSessions[target]
	b.mu.RUnlock()
	return session, ok && session.URL != "" && time.Now().UTC().Before(session.ExpiresAt)
}

func (b *Bot) pruneSessionsLocked(now time.Time) {
	for target, session := range b.dmSessions {
		if !now.Before(session.ExpiresAt) {
			delete(b.dmSessions, target)
		}
	}
	for target, session := range b.groupSessions {
		if !now.Before(session.ExpiresAt) {
			delete(b.groupSessions, target)
		}
	}
}

func dingTalkEventTime(milliseconds int64) time.Time {
	if milliseconds <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(milliseconds).UTC()
}

func (b *Bot) markSeen(messageID string) bool {
	if messageID == "" {
		return false
	}
	now := time.Now().UTC()
	b.mu.Lock()
	defer b.mu.Unlock()
	if seenAt, ok := b.seenMessages[messageID]; ok && now.Sub(seenAt) < seenMessageTTL {
		return true
	}
	for id, seenAt := range b.seenMessages {
		if now.Sub(seenAt) >= seenMessageTTL {
			delete(b.seenMessages, id)
		}
	}
	b.seenMessages[messageID] = now
	return false
}

func (b *Bot) wasSeen(messageID string) bool {
	if messageID == "" {
		return false
	}
	now := time.Now().UTC()
	b.mu.Lock()
	defer b.mu.Unlock()
	seenAt, ok := b.seenMessages[messageID]
	return ok && now.Sub(seenAt) < seenMessageTTL
}

func (b *Bot) registerBotIdentity(botID string) {
	if botID == "" {
		return
	}
	b.mu.Lock()
	if _, ok := b.registeredBots[botID]; ok || b.finalized {
		b.mu.Unlock()
		return
	}
	b.registeredBots[botID] = struct{}{}
	b.mu.Unlock()
	if registrar, ok := b.handler.(channel.BotRegistrar); ok {
		registrar.RegisterBotIdentity(channel.PlatformDingTalk, botID, b.Name())
	}
}

func (b *Bot) pollStopped() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.ctx != nil && b.ctx.Err() != nil
}

func orderedIDs(ids ...string) []string {
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func dingTalkMentions(users []chatbot.BotCallbackDataAtUserModel) []channel.Mention {
	out := make([]channel.Mention, 0, len(users))
	for _, user := range users {
		id := user.StaffId
		if id == "" {
			id = user.DingtalkId
		}
		if id != "" {
			out = append(out, channel.Mention{Raw: "@" + id, PlatformID: id})
		}
	}
	return out
}

func collectStream(ctx context.Context, stream *channel.ChatStream) (string, error) {
	var text strings.Builder
	var streamErr error
	for {
		select {
		case <-ctx.Done():
			return text.String(), ctx.Err()
		case event, ok := <-stream.Events:
			if !ok {
				return text.String(), streamErr
			}
			text.WriteString(event.Text)
			if event.Err != nil {
				streamErr = event.Err
			}
		}
	}
}
