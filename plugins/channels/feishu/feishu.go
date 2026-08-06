package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"

	internalchannel "github.com/CherryHQ/stella/internal/channel"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/channel"
)

const feishuMaxMessageLen = 4000

// seenMsgTTL is how long message IDs are retained for dedup.
// Feishu retries within ~30s, so 5 minutes is generous.
const seenMsgTTL = 5 * time.Minute

// mentionPlaceholderRegex matches @_user_N placeholders inserted by Feishu for mentions.
var mentionPlaceholderRegex = regexp.MustCompile(`@_user_\d+`)

func logger() *slog.Logger { return slog.With("component", "feishu") }

// GroupConfig holds per-group overrides, keyed by chat_id in Config.Groups.
type GroupConfig struct {
	SystemPrompt string   `json:"system_prompt"` // prepend to user message in this group
	ToolAllow    []string `json:"tool_allow"`    // reserved: only these tools (not yet enforced)
	ToolDeny     []string `json:"tool_deny"`     // reserved: deny these tools (not yet enforced)
}

// Config holds Feishu bot settings.
type Config struct {
	InstanceID        string                 `json:"-"`
	AppID             string                 `json:"app_id"`
	AppSecret         string                 `json:"app_secret"`
	EncryptKey        string                 `json:"encrypt_key"`
	VerificationToken string                 `json:"verification_token"`
	Groups            map[string]GroupConfig `json:"groups"` // per-group overrides keyed by chat_id
	TenantKey         string                 `json:"tenant_key"`
	AutoProvision     bool                   `json:"auto_provision"`
}

// Bot wraps a Feishu bot with agent pool integration.
type listChatsFunc func(context.Context, *larkim.ListChatReq) (*larkim.ListChatResp, error)

type Bot struct {
	client    *lark.Client
	wsClient  *larkws.Client
	listChats listChatsFunc
	handler   channel.Handler

	botOpenID atomic.Value // bot's own open_id (string), fetched on startup

	mu            sync.RWMutex
	chatModels    map[string]channel.ModelOption
	seenMsgs      map[string]time.Time // message ID -> first seen time
	lastSeenSweep time.Time            // last time seenMsgs was swept

	provisionedMu sync.Mutex
	provisioned   map[string]time.Time // union_id -> last provision time (1h TTL)

	learnedTenantKeyMu sync.RWMutex
	learnedTenantKey   string // tenant_key auto-detected at startup via tenant API

	chatTypes sync.Map // chatID -> "p2p" | "group", cached from Get Chat API
	unionIDs  sync.Map // openID -> unionID, populated by onMessage for card action lookups

	cfg    Config
	ctx    context.Context
	cancel context.CancelFunc

	routingMu        sync.Mutex
	routingFinalized bool
	registeredBotID  string
}

// New creates a Feishu bot. Call Start to begin receiving events.
func New(cfg Config, handler channel.Handler) (*Bot, error) {
	if cfg.AppID == "" || cfg.AppSecret == "" {
		return nil, fmt.Errorf("feishu: app_id and app_secret are required")
	}

	b := &Bot{
		handler:     handler,
		chatModels:  make(map[string]channel.ModelOption),
		seenMsgs:    make(map[string]time.Time),
		provisioned: make(map[string]time.Time),
		cfg:         cfg,
	}
	if registrar, ok := handler.(interface {
		RegisterGroupPublisher(string, internalchannel.GroupPublisher)
	}); ok {
		registrar.RegisterGroupPublisher(b.Name(), b)
	}

	return b, nil
}

// Start initializes the API client, registers event handlers, and starts
// a WebSocket connection. It blocks until ctx is cancelled.
func (b *Bot) Start(ctx context.Context) error {
	b.ctx, b.cancel = context.WithCancel(ctx)

	b.client = lark.NewClient(b.cfg.AppID, b.cfg.AppSecret,
		lark.WithLogLevel(larkcore.LogLevelInfo),
		lark.WithEnableTokenCache(true),
	)

	if err := b.fetchBotOpenID(b.ctx); err != nil {
		logger().Warn("failed to fetch bot open_id, self-message filtering disabled", "error", err)
	} else if registrar, ok := b.handler.(channel.BotRegistrar); ok {
		if botID, _ := b.botOpenID.Load().(string); botID != "" {
			b.routingMu.Lock()
			if !b.routingFinalized {
				registrar.RegisterBotIdentity(channel.PlatformFeishu, botID, b.cfg.InstanceID)
				b.registeredBotID = botID
			}
			b.routingMu.Unlock()
		}
	}

	if b.cfg.AutoProvision && b.cfg.TenantKey == "" {
		if err := b.fetchBotTenantKey(b.ctx); err != nil {
			logger().Warn("auto-provision: failed to detect tenant_key at startup, configure it explicitly", "error", err)
		}
	}

	eventHandler := dispatcher.NewEventDispatcher(b.cfg.VerificationToken, b.cfg.EncryptKey).
		OnP2MessageReceiveV1(b.onMessage).
		OnP2MessageReactionCreatedV1(b.onReaction).
		OnP2MessageReactionDeletedV1(b.onReactionDeleted).
		OnP2MessageReadV1(b.onMessageRead).
		OnP2ChatMemberBotAddedV1(b.onBotAdded).
		OnP2ChatMemberBotDeletedV1(b.onBotDeleted).
		OnP2CardActionTrigger(b.onCardAction).
		OnP2ChatAccessEventBotP2pChatEnteredV1(func(_ context.Context, _ *larkim.P2ChatAccessEventBotP2pChatEnteredV1) error {
			return nil
		})

	b.wsClient = larkws.NewClient(b.cfg.AppID, b.cfg.AppSecret,
		larkws.WithEventHandler(eventHandler),
		larkws.WithLogLevel(larkcore.LogLevelInfo),
	)
	if b.listChats == nil {
		b.listChats = func(ctx context.Context, req *larkim.ListChatReq) (*larkim.ListChatResp, error) {
			return b.client.Im.Chat.List(ctx, req)
		}
	}

	go b.syncGroups()

	logger().Info("feishu bot starting (WebSocket mode)")

	errCh := make(chan error, 1)
	go func() {
		errCh <- b.wsClient.Start(b.ctx)
	}()

	select {
	case <-b.ctx.Done():
		return b.ctx.Err()
	case err := <-errCh:
		return fmt.Errorf("feishu: websocket error: %w", err)
	}
}

// Stop cancels the bot context.
func (b *Bot) Stop() {
	logger().Info("stopping feishu bot")
	if b.cancel != nil {
		b.cancel()
	}
}

// Finalize removes routing registrations after accepted work has drained.
func (b *Bot) Finalize() {
	b.routingMu.Lock()
	defer b.routingMu.Unlock()
	if b.routingFinalized {
		return
	}
	b.routingFinalized = true
	if b.registeredBotID != "" {
		if registrar, ok := b.handler.(interface {
			UnregisterBotIdentity(string, string, string)
		}); ok {
			registrar.UnregisterBotIdentity(channel.PlatformFeishu, b.registeredBotID, b.cfg.InstanceID)
		}
	}
	if registrar, ok := b.handler.(interface{ UnregisterGroupPublisher(string) }); ok {
		registrar.UnregisterGroupPublisher(b.Name())
	}
}

// fetchBotOpenID calls the Feishu bot info API to retrieve and store the bot's open_id.
func (b *Bot) fetchBotOpenID(ctx context.Context) error {
	resp, err := b.client.Do(ctx, &larkcore.ApiReq{
		HttpMethod:                http.MethodGet,
		ApiPath:                   "/open-apis/bot/v3/info",
		SupportedAccessTokenTypes: []larkcore.AccessTokenType{larkcore.AccessTokenTypeTenant},
	})
	if err != nil {
		return fmt.Errorf("bot info request: %w", err)
	}

	var result struct {
		Code int `json:"code"`
		Bot  struct {
			OpenID string `json:"open_id"`
		} `json:"bot"`
	}
	if err := json.Unmarshal(resp.RawBody, &result); err != nil {
		return fmt.Errorf("bot info parse: %w", err)
	}
	if result.Code != 0 {
		return fmt.Errorf("bot info api error (code=%d)", result.Code)
	}
	if result.Bot.OpenID == "" {
		return fmt.Errorf("bot info: empty open_id")
	}

	b.botOpenID.Store(result.Bot.OpenID)
	logger().Info("fetched bot open_id", "open_id", result.Bot.OpenID)
	return nil
}

// Name returns the backend name. Implements channel.Channel.
func (b *Bot) Name() string {
	if b.cfg.InstanceID != "" {
		return b.cfg.InstanceID
	}
	return channel.PlatformFeishu
}

func (b *Bot) Platform() string { return channel.PlatformFeishu }

// Notify sends a notification message. Implements channel.Channel.
// Supports chat IDs (oc_ prefix), union IDs (on_ prefix), and open IDs (ou_ prefix).
// Open IDs are promoted to union IDs via the Contact API before sending; if
// that fails, callers must pass a union ID instead because open IDs are app-scoped.
// When the text contains {{button ...}} directives, sends an interactive card
// so buttons render as clickable elements; otherwise sends plain text.
func (b *Bot) Notify(ctx context.Context, n channel.Notification) error {
	chatID := n.ChatID
	if chatID == "" {
		return fmt.Errorf("feishu: no target chat ID")
	}

	// Strip channel prefix if present.
	chatID = strings.TrimPrefix(chatID, "feishu:")

	// Promote open_id to union_id so we use the stable cross-app ID.
	if strings.HasPrefix(chatID, "ou_") {
		if unionID := b.resolveUnionID(ctx, chatID); unionID != "" {
			logger().Debug("notify: promoted open_id to union_id", "open_id", chatID, "union_id", unionID)
			chatID = unionID
		} else {
			return fmt.Errorf("feishu: notify: failed to resolve union_id for open_id %q; pass a union_id (on_...) resolved through the same app's directory because open_id is app-scoped", chatID)
		}
	}

	receiveIDType := receiveIDTypeForChatID(chatID)

	msgType := larkim.MsgTypeText
	content := textContent(n.Text)
	if cardButtonDirective.MatchString(n.Text) {
		if card, err := buildCardContent(n.Text); err == nil {
			msgType = larkim.MsgTypeInteractive
			content = card
		} else {
			content = textContent(stripCardDirectives(n.Text))
		}
	}

	logger().Debug("notify sending message",
		"instance", b.Name(), "chat_id", chatID, "msg_type", msgType)

	resp, err := b.client.Im.Message.Create(ctx,
		larkim.NewCreateMessageReqBuilder().
			ReceiveIdType(receiveIDType).
			Body(larkim.NewCreateMessageReqBodyBuilder().
				MsgType(msgType).
				ReceiveId(chatID).
				Content(content).
				Build()).
			Build())
	if err != nil {
		return fmt.Errorf("feishu: notify: %w", err)
	}
	if !resp.Success() {
		return fmt.Errorf("feishu: notify: code=%d msg=%s", resp.Code, resp.Msg)
	}
	return nil
}

// receiveIDTypeForChatID returns the Feishu ReceiveIdType for the given ID.
// ou_ → open_id, on_ → union_id (stored by auto-provision), oc_ and others → chat_id.
func receiveIDTypeForChatID(chatID string) string {
	switch {
	case strings.HasPrefix(chatID, "ou_"):
		return larkim.ReceiveIdTypeOpenId
	case strings.HasPrefix(chatID, "on_"):
		return larkim.ReceiveIdTypeUnionId
	default:
		return larkim.ReceiveIdTypeChatId
	}
}

// markSeen records a message ID and returns true if it was already seen.
// Evicts entries older than seenMsgTTL periodically to bound memory usage.
func (b *Bot) markSeen(messageID string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := time.Now()

	// Sweep expired entries at most once per minute to avoid O(N) on every message.
	if now.Sub(b.lastSeenSweep) > time.Minute {
		for id, t := range b.seenMsgs {
			if now.Sub(t) > seenMsgTTL {
				delete(b.seenMsgs, id)
			}
		}
		b.lastSeenSweep = now
	}

	if _, ok := b.seenMsgs[messageID]; ok {
		return true
	}
	b.seenMsgs[messageID] = now
	return false
}

// stripMentions removes @mention placeholders from message text.
// First removes known mention keys, then cleans up any remaining @_user_N patterns.
func stripMentions(text string, mentions []*larkim.MentionEvent) string {
	for _, m := range mentions {
		if m.Key != nil {
			text = strings.ReplaceAll(text, *m.Key, "")
		}
	}
	text = mentionPlaceholderRegex.ReplaceAllString(text, "")
	return strings.TrimSpace(text)
}

// incomingMsg builds an IncomingMessage from the Feishu context.
func (b *Bot) incomingMsg(senderIDs []string, chatID string, chatType string, content []ai.ContentBlock) channel.IncomingMessage {
	senderIDs = feishuSenderIDs(senderIDs...)
	senderID := ""
	if len(senderIDs) > 0 {
		senderID = senderIDs[0]
	}
	return channel.IncomingMessage{
		Platform:  channel.PlatformFeishu,
		ChannelID: b.Name(),
		SenderID:  senderID,
		SenderIDs: append([]string(nil), senderIDs...),
		ChatID:    chatID,
		IsGroup:   chatType == "group",
		Content:   content,
	}
}

// feishuEventTime parses Feishu's millisecond-epoch string into a time.Time.
// Returns the zero time if the input is empty or unparseable.
func feishuEventTime(ms string) time.Time {
	if ms == "" {
		return time.Time{}
	}
	n, err := strconv.ParseInt(ms, 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.UnixMilli(n).UTC()
}

// feishuMentions normalizes Feishu mention events. AgentID is left empty — the
// dispatcher resolves it from group membership.
func feishuMentions(mentions []*larkim.MentionEvent) []channel.Mention {
	var out []channel.Mention
	for _, m := range mentions {
		if m == nil {
			continue
		}
		raw := derefStr(m.Name)
		if raw == "" {
			raw = derefStr(m.Key)
		}
		platformID := ""
		if m.Id != nil {
			platformID = derefStr(m.Id.OpenId)
		}
		out = append(out, channel.Mention{Raw: raw, PlatformID: platformID})
	}
	return out
}

func feishuSenderIDs(ids ...string) []string {
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

func senderIDsFromUserID(userID *larkim.UserId) []string {
	if userID == nil {
		return nil
	}
	return feishuSenderIDs(derefStr(userID.UnionId), derefStr(userID.OpenId))
}
