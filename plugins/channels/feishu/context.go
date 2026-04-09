package feishu

import (
	"context"
	"time"
)

const (
	feishuAPITimeout       = 30 * time.Second
	feishuOperationTimeout = 15 * time.Minute
)

func (b *Bot) apiContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), feishuAPITimeout)
}

func (b *Bot) operationContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), feishuOperationTimeout)
}
