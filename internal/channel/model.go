package channel

import "context"

// Platform identifiers for each messaging channel.
const (
	PlatformTelegram = "telegram"
	PlatformQQ       = "qq"
	PlatformFeishu   = "feishu"
	PlatformWeixin   = "weixin"
	PlatformCLI      = "cli"
)

// Channel is a messaging platform that receives user messages and sends notifications.
type Channel interface {
	// Name returns a unique identifier (e.g. "telegram", "qq").
	Name() string

	// Start begins listening for messages. Blocks until ctx is cancelled.
	Start(ctx context.Context) error

	// Stop gracefully shuts down the channel.
	Stop()

	// Notify sends a push notification to a target within this channel.
	Notify(ctx context.Context, n Notification) error
}

// ModelOption represents a selectable provider/model combination.
type ModelOption struct {
	Provider string
	Model    string
}

// ModelListFunc returns the current list of available models.
// Called on demand so callers always see the latest cached models.
type ModelListFunc func() []ModelOption

// ModelSwitchFunc switches the active model in the pool.
// It rebuilds the runner factory for the given provider/model pair.
type ModelSwitchFunc func(provider, model string) error
