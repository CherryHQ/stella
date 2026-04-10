package channel

// TelegramConfig is the persisted Telegram channel plugin configuration.
type TelegramConfig struct {
	Token        string `json:"token"`
	ChannelID    string `json:"channel_id"`
	GroupMode    string `json:"group_mode"`
	EnableNotify bool   `json:"enable_notify"`
}

// QQConfig is the persisted QQ channel plugin configuration.
type QQConfig struct {
	AppID        string `json:"app_id"`
	AppSecret    string `json:"app_secret"`
	GroupMode    string `json:"group_mode"`
	EnableNotify bool   `json:"enable_notify"`
}

// FeishuGroup is a per-chat override in the persisted Feishu channel config.
type FeishuGroup struct {
	GroupMode    string   `json:"group_mode"`
	SystemPrompt string   `json:"system_prompt"`
	ToolAllow    []string `json:"tool_allow"`
	ToolDeny     []string `json:"tool_deny"`
}

// FeishuConfig is the persisted Feishu channel plugin configuration.
type FeishuConfig struct {
	AppID             string                 `json:"app_id"`
	AppSecret         string                 `json:"app_secret"`
	EncryptKey        string                 `json:"encrypt_key"`
	VerificationToken string                 `json:"verification_token"`
	GroupMode         string                 `json:"group_mode"`
	Groups            map[string]FeishuGroup `json:"groups"`
	EnableNotify      bool                   `json:"enable_notify"`
}

// WeixinConfig is the persisted Weixin channel plugin configuration.
type WeixinConfig struct {
	BotToken     string `json:"bot_token"`
	BaseURL      string `json:"base_url"`
	BotID        string `json:"bot_id"`
	UserID       string `json:"user_id"`
	EnableNotify bool   `json:"enable_notify"`
}
