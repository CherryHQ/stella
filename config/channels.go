package config

// ChannelsConfig groups all channel (interface) configurations.
type ChannelsConfig struct {
	Telegram TelegramConfig `yaml:"telegram" envPrefix:"TELEGRAM_"`
	QQ       QQConfig       `yaml:"qq"       envPrefix:"QQ_"`
	Feishu   FeishuConfig   `yaml:"feishu"   envPrefix:"FEISHU_"`
}

type TelegramConfig struct {
	Enabled      *bool   `yaml:"enabled"       env:"ENABLED"`
	EnableNotify *bool   `yaml:"enable_notify" env:"ENABLE_NOTIFY"`
	Token        string  `yaml:"token"         env:"TOKEN"`
	NotifyChat   string  `yaml:"notify_chat"   env:"NOTIFY_CHAT"`
	ChannelID    string  `yaml:"channel_id"    env:"CHANNEL_ID"`
	GroupMode    string  `yaml:"group_mode"    env:"GROUP_MODE"`
	AllowedIDs   []int64 `yaml:"allowed_ids"   env:"ALLOWED_IDS"`
}

func (c TelegramConfig) IsEnabled() bool       { return boolDefault(c.Enabled, true) }
func (c TelegramConfig) IsNotifyEnabled() bool { return boolDefault(c.EnableNotify, false) }

type QQConfig struct {
	Enabled      *bool    `yaml:"enabled"       env:"ENABLED"`
	EnableNotify *bool    `yaml:"enable_notify" env:"ENABLE_NOTIFY"`
	AppID        string   `yaml:"app_id"        env:"APP_ID"`
	AppSecret    string   `yaml:"app_secret"    env:"APP_SECRET"`
	GroupMode    string   `yaml:"group_mode"    env:"GROUP_MODE"`
	AllowedIDs   []string `yaml:"allowed_ids"   env:"ALLOWED_IDS"`
}

func (c QQConfig) IsEnabled() bool       { return boolDefault(c.Enabled, true) }
func (c QQConfig) IsNotifyEnabled() bool { return boolDefault(c.EnableNotify, false) }

type FeishuConfig struct {
	Enabled           *bool    `yaml:"enabled"            env:"ENABLED"`
	EnableNotify      *bool    `yaml:"enable_notify"      env:"ENABLE_NOTIFY"`
	AppID             string   `yaml:"app_id"             env:"APP_ID"`
	AppSecret         string   `yaml:"app_secret"         env:"APP_SECRET"`
	EncryptKey        string   `yaml:"encrypt_key"        env:"ENCRYPT_KEY"`
	VerificationToken string   `yaml:"verification_token" env:"VERIFICATION_TOKEN"`
	NotifyChat        string   `yaml:"notify_chat"        env:"NOTIFY_CHAT"`
	GroupMode         string   `yaml:"group_mode"         env:"GROUP_MODE"`
	AllowedIDs        []string `yaml:"allowed_ids"        env:"ALLOWED_IDS"`
}

func (c FeishuConfig) IsEnabled() bool       { return boolDefault(c.Enabled, true) }
func (c FeishuConfig) IsNotifyEnabled() bool { return boolDefault(c.EnableNotify, false) }

// boolDefault dereferences a *bool pointer, returning def if the pointer is nil.
func boolDefault(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}
