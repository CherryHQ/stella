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

	// Hard ceilings on the admin-configured timeouts: the wait timeout holds
	// an HTTP connection open, the run timeout holds an in-flight run slot.
	// Raise if a legitimate longer-running webhook workload appears.
	WebhookWaitTimeoutCeilingSeconds = 600
	WebhookRunTimeoutCeilingSeconds  = 3600
)

// Webhook endpoint providers. The endpoint's durable provider binding is
// immutable while active; this config selects which provider may be issued and
// holds the non-secret GitHub delivery allowlists.
const (
	WebhookProviderGeneric = "generic"
	WebhookProviderGitHub  = "github"
)

// WebhookConfig is the persisted config for the inbound webhook channel.
// The agent binding lives on the channel row (agent_id), not here. Provider
// secrets live only in the encrypted endpoint record; config holds behavior and
// GitHub's non-secret allowlists.
type WebhookConfig struct {
	// Provider selects generic capability-only or GitHub HMAC verification.
	Provider string `json:"provider"`
	// GitHubEvents and GitHubRepositories constrain signed GitHub deliveries.
	// Both must be non-empty for the GitHub provider.
	GitHubEvents       []string `json:"github_events"`
	GitHubRepositories []string `json:"github_repositories"`
	// DefaultWait selects synchronous (true) vs. fire-and-forget (false) when a
	// request does not set the ?wait query parameter.
	DefaultWait bool `json:"default_wait"`
	// WaitTimeoutSeconds bounds how long a synchronous caller waits for the
	// agent's reply before receiving 504 (the run continues in the background).
	WaitTimeoutSeconds int `json:"wait_timeout_seconds"`
	// MaxRunTimeoutSeconds is the hard ceiling on the agent run itself.
	MaxRunTimeoutSeconds int `json:"max_run_timeout_seconds"`
	// SessionMode is "ephemeral" (fresh session per trigger, default) or
	// "persistent" (one stable session per caller per webhook instance).
	SessionMode string `json:"session_mode"`
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

// Persistent reports whether triggers accumulate into one stable session.
func (c WebhookConfig) Persistent() bool {
	return c.SessionMode == WebhookSessionPersistent
}
