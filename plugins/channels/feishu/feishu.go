package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
	"github.com/vaayne/anna/pkg/ai"
	"github.com/vaayne/anna/pkg/channel"
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
	GroupMode    string   `json:"group_mode"`    // override global group_mode for this group
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
	GroupMode         string                 `json:"group_mode"` // "mention" | "always" | "disabled"
	Groups            map[string]GroupConfig `json:"groups"`     // per-group overrides keyed by chat_id
	TenantKey         string                 `json:"tenant_key"`
	AutoProvision     bool                   `json:"auto_provision"`
}

// Bot wraps a Feishu bot with agent pool integration.
type Bot struct {
	client   *lark.Client
	wsClient *larkws.Client
	handler  channel.Handler

	botOpenID atomic.Value // bot's own open_id (string), fetched on startup

	mu            sync.RWMutex
	chatModels    map[string]channel.ModelOption
	seenMsgs      map[string]time.Time // message ID -> first seen time
	lastSeenSweep time.Time            // last time seenMsgs was swept

	provisionedMu sync.Mutex
	provisioned   map[string]time.Time // union_id -> last provision time (1h TTL)

	learnedTenantKeyMu sync.RWMutex
	learnedTenantKey   string // tenant_key auto-detected at startup via tenant API

	cfg    Config
	ctx    context.Context
	cancel context.CancelFunc
}

// New creates a Feishu bot. Call Start to begin receiving events.
func New(cfg Config, handler channel.Handler) (*Bot, error) {
	if cfg.AppID == "" || cfg.AppSecret == "" {
		return nil, fmt.Errorf("feishu: app_id and app_secret are required")
	}

	if cfg.GroupMode == "" {
		cfg.GroupMode = "mention"
	}

	b := &Bot{
		handler:     handler,
		chatModels:  make(map[string]channel.ModelOption),
		seenMsgs:    make(map[string]time.Time),
		provisioned: make(map[string]time.Time),
		cfg:         cfg,
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
	}

	if b.cfg.AutoProvision && b.cfg.TenantKey == "" {
		if err := b.fetchBotTenantKey(b.ctx); err != nil {
			logger().Warn("auto-provision: failed to detect tenant_key at startup, configure it explicitly", "error", err)
		}
	}

	eventHandler := dispatcher.NewEventDispatcher(b.cfg.VerificationToken, b.cfg.EncryptKey).
		OnP2MessageReceiveV1(b.onMessage).
		OnP2MessageReactionCreatedV1(b.onReaction)

	b.wsClient = larkws.NewClient(b.cfg.AppID, b.cfg.AppSecret,
		larkws.WithEventHandler(eventHandler),
		larkws.WithLogLevel(larkcore.LogLevelInfo),
	)

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
// Supports both chat IDs (oc_ prefix) and user open IDs (ou_ prefix).
func (b *Bot) Notify(ctx context.Context, n channel.Notification) error {
	chatID := n.ChatID
	if chatID == "" {
		return fmt.Errorf("feishu: no target chat ID")
	}

	// Strip channel prefix if present.
	chatID = strings.TrimPrefix(chatID, "feishu:")

	// Detect receiver type from ID prefix.
	receiveIDType := larkim.ReceiveIdTypeChatId
	if strings.HasPrefix(chatID, "ou_") {
		receiveIDType = larkim.ReceiveIdTypeOpenId
	}

	content := textContent(n.Text)
	resp, err := b.client.Im.Message.Create(ctx,
		larkim.NewCreateMessageReqBuilder().
			ReceiveIdType(receiveIDType).
			Body(larkim.NewCreateMessageReqBodyBuilder().
				MsgType(larkim.MsgTypeText).
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

// textContent builds the JSON content string for a Feishu text message.
func textContent(text string) string {
	data, _ := json.Marshal(map[string]string{"text": text})
	return string(data)
}

// cardContent builds a Feishu Interactive Card JSON 2.0 string with markdown content.
// Cards support the Patch API for in-place editing (plain text messages do not).
func cardContent(text string) string {
	card := map[string]any{
		"schema": "2.0",
		"body": map[string]any{
			"elements": []map[string]any{
				{
					"tag":     "markdown",
					"content": text,
				},
			},
		},
	}
	data, _ := json.Marshal(card)
	return string(data)
}

// groupMode returns the effective group mode for the given chat.
// Per-group config overrides the global setting.
func (b *Bot) groupMode(chatID string) string {
	if gc, ok := b.cfg.Groups[chatID]; ok && gc.GroupMode != "" {
		return gc.GroupMode
	}
	return b.cfg.GroupMode
}

// shouldRespondInGroup checks whether the bot should respond based on group_mode
// and whether it was mentioned. Uses per-group config when available.
func (b *Bot) shouldRespondInGroup(chatID string, mentions []*larkim.MentionEvent) bool {
	switch b.groupMode(chatID) {
	case "disabled":
		return false
	case "always":
		return true
	default: // "mention"
		return b.isBotMentioned(mentions)
	}
}

// groupSystemPrompt returns the system prompt override for the given chat, if any.
func (b *Bot) groupSystemPrompt(chatID string) string {
	if gc, ok := b.cfg.Groups[chatID]; ok {
		return gc.SystemPrompt
	}
	return ""
}

// isBotMentioned checks if the bot was @mentioned by comparing each mention's
// open_id against the bot's own open_id (fetched on startup).
func (b *Bot) isBotMentioned(mentions []*larkim.MentionEvent) bool {
	if len(mentions) == 0 {
		return false
	}

	knownID, _ := b.botOpenID.Load().(string)
	if knownID == "" {
		// Fallback: if bot open_id is unknown, assume any non-@all mention is the bot.
		for _, m := range mentions {
			if m.Key != nil && *m.Key != "@_all" {
				return true
			}
		}
		return false
	}

	for _, m := range mentions {
		if m.Id == nil {
			continue
		}
		if m.Id.OpenId != nil && *m.Id.OpenId == knownID {
			return true
		}
	}
	return false
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
