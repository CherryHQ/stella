package qq

import (
	"cmp"
	"context"
	"time"

	"github.com/tencent-connect/botgo"
	"github.com/tencent-connect/botgo/token"

	"github.com/CherryHQ/stella/pkg/channel"
)

// NewDurableGroupPublisher constructs OpenAPI egress without a WebSocket
// session or a background token-refresh goroutine.
func NewDurableGroupPublisher(cfg Config) (channel.GroupPublisher, error) {
	b, err := New(cfg, nil)
	if err != nil {
		return nil, err
	}
	b.creds = &token.QQBotCredentials{AppID: cfg.AppID, AppSecret: cfg.AppSecret}
	b.tokenSource = token.NewQQBotTokenSource(b.creds)
	b.api = botgo.NewOpenAPI(b.creds.AppID, b.tokenSource).WithTimeout(10 * time.Second)
	return b, nil
}

func (b *Bot) PublishIncoming(ctx context.Context, req channel.DurablePublishRequest) error {
	scope := scopeC2C
	if req.IsGroup {
		scope = scopeGroup
	}
	return b.publish(ctx, channel.GroupPublishRequest{
		Platform: req.Platform, PlatformGroupID: cmp.Or(req.ChatID, req.TargetID),
		ReplyTo: cmp.Or(req.MessageID, req.ReplyTo), DeliveryID: req.DeliveryID,
		Stream: req.Stream,
	}, scope)
}
