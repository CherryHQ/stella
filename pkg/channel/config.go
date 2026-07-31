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

// Webhook session modes.
const (
	WebhookSessionEphemeral  = "ephemeral"
	WebhookSessionPersistent = "persistent"

	WebhookDefaultWaitTimeoutSeconds   = 60
	WebhookDefaultMaxRunTimeoutSeconds = 300

	// Hard ceilings on persisted server-side timeouts: the wait timeout holds
	// an HTTP connection open, the run timeout holds an in-flight run slot.
	// Raise if a legitimate longer-running webhook workload appears.
	WebhookWaitTimeoutCeilingSeconds = 600
	WebhookRunTimeoutCeilingSeconds  = 3600
)

// WebhookConfig is the persisted server-side ceiling configuration. Invocation
// behaviour (wait and session_mode) is supplied by the capability request.
type WebhookConfig struct {
	// WaitTimeoutSeconds bounds a synchronous caller's reply wait.
	WaitTimeoutSeconds int `json:"wait_timeout_seconds"`
	// MaxRunTimeoutSeconds is the hard ceiling on the agent run itself.
	MaxRunTimeoutSeconds int `json:"max_run_timeout_seconds"`
}

// EffectiveWaitTimeout returns the configured wait timeout or the default.
func (c WebhookConfig) EffectiveWaitTimeout() int {
	if c.WaitTimeoutSeconds > 0 {
		return c.WaitTimeoutSeconds
	}
	return WebhookDefaultWaitTimeoutSeconds
}

// EffectiveMaxRunTimeout returns the configured run ceiling or the default.
func (c WebhookConfig) EffectiveMaxRunTimeout() int {
	if c.MaxRunTimeoutSeconds > 0 {
		return c.MaxRunTimeoutSeconds
	}
	return WebhookDefaultMaxRunTimeoutSeconds
}
