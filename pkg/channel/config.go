package channel

// TelegramConfig is the persisted Telegram channel plugin configuration.
type TelegramConfig struct {
	InstanceID   string `json:"-"`
	Token        string `json:"token"`
	ChannelID    string `json:"channel_id"`
	EnableNotify bool   `json:"enable_notify"`
}

// QQConfig is the persisted QQ channel plugin configuration.
type QQConfig struct {
	InstanceID   string `json:"-"`
	AppID        string `json:"app_id"`
	AppSecret    string `json:"app_secret"`
	EnableNotify bool   `json:"enable_notify"`
}

// FeishuGroup is a per-chat override in the persisted Feishu channel config.
type FeishuGroup struct {
	SystemPrompt string   `json:"system_prompt"`
	ToolAllow    []string `json:"tool_allow"`
	ToolDeny     []string `json:"tool_deny"`
}

// FeishuConfig is the persisted Feishu channel plugin configuration.
type FeishuConfig struct {
	InstanceID        string                 `json:"-"`
	AppID             string                 `json:"app_id"`
	AppSecret         string                 `json:"app_secret"`
	EncryptKey        string                 `json:"encrypt_key"`
	VerificationToken string                 `json:"verification_token"`
	Groups            map[string]FeishuGroup `json:"groups"`
	EnableNotify      bool                   `json:"enable_notify"`
	TenantKey         string                 `json:"tenant_key"`
	AutoProvision     bool                   `json:"auto_provision"`
}

// WeixinConfig is the persisted Weixin channel plugin configuration.
type WeixinConfig struct {
	InstanceID   string `json:"-"`
	BotToken     string `json:"bot_token"`
	BaseURL      string `json:"base_url"`
	BotID        string `json:"bot_id"`
	UserID       string `json:"user_id"`
	SKRouteTag   string `json:"sk_route_tag"`
	EnableNotify bool   `json:"enable_notify"`
}

// DiscordConfig is the persisted Discord channel plugin configuration.
type DiscordConfig struct {
	InstanceID string `json:"-"`
	Token      string `json:"token"`
	GuildID    string `json:"guild_id"`   // optional: restrict ingress to one server
	ChannelID  string `json:"channel_id"` // optional default notify channel
	// MentionOnly gates guild ingress: when true (the safe default, applied when
	// the key is absent) the adapter forwards a guild message only when the bot is
	// @-mentioned or the channel is whitelisted. An explicit false opts into
	// responding to every message in scope.
	MentionOnly bool `json:"mention_only"`
	// RespondChannels is a comma-separated list of channel snowflakes that bypass
	// MentionOnly. It is stored as a string (not []string) so it round-trips
	// through the frontend's string/boolean config serializer; the adapter splits
	// it into a slice at use.
	RespondChannels string `json:"respond_channels"`
	EnableNotify    bool   `json:"enable_notify"` // parity only; gates nothing
}
