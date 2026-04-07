package channel

// TelegramConfig is the persisted Telegram channel plugin configuration.
type TelegramConfig struct {
	Token        string `json:"token"`
	ChannelID    string `json:"channel_id"`
	GroupMode    string `json:"group_mode"`
	EnableNotify bool   `json:"enable_notify"`
}
