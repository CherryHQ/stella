package feishu

import (
	"cmp"
	"context"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"

	"github.com/CherryHQ/stella/pkg/channel"
)

// NewDurableGroupPublisher constructs HTTP egress without starting a WebSocket
// or registering a listener-local publisher.
func NewDurableGroupPublisher(cfg Config) (channel.GroupPublisher, error) {
	b, err := New(cfg, nil, nil)
	if err != nil {
		return nil, err
	}
	b.client = lark.NewClient(cfg.AppID, cfg.AppSecret,
		lark.WithLogLevel(larkcore.LogLevelInfo), lark.WithEnableTokenCache(true))
	return b, nil
}

func (b *Bot) PublishIncoming(ctx context.Context, req channel.DurablePublishRequest) error {
	return b.Publish(ctx, channel.GroupPublishRequest{
		Platform: req.Platform, PlatformGroupID: cmp.Or(req.ChatID, req.TargetID),
		PlatformThreadID: req.ThreadID, ReplyTo: cmp.Or(req.MessageID, req.ReplyTo),
		DeliveryID: req.DeliveryID, RequesterID: req.TargetID,
		LifecycleFeedback: req.LifecycleFeedback, Stream: req.Stream,
	})
}
