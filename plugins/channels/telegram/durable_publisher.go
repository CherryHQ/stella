package telegram

import (
	"cmp"
	"context"
	"fmt"
	"strings"

	tgmd "github.com/Mad-Pixels/goldmark-tgmd"
	tele "gopkg.in/telebot.v4"

	"github.com/CherryHQ/stella/pkg/channel"
)

// NewDurableGroupPublisher constructs egress without a poller or eager getMe
// request. Only Publish may contact Telegram for this delivery attempt.
func NewDurableGroupPublisher(cfg Config) (channel.GroupPublisher, error) {
	if strings.TrimSpace(cfg.Token) == "" {
		return nil, fmt.Errorf("telegram: bot token is required")
	}
	bot, err := tele.NewBot(tele.Settings{Token: cfg.Token, Offline: true})
	if err != nil {
		return nil, fmt.Errorf("create Telegram durable publisher: %w", err)
	}
	return &Bot{bot: bot, md: tgmd.TGMD(), cfg: cfg}, nil
}

func (b *Bot) PublishIncoming(ctx context.Context, req channel.DurablePublishRequest) error {
	return b.Publish(ctx, channel.GroupPublishRequest{
		Platform: req.Platform, PlatformGroupID: cmp.Or(req.ChatID, req.TargetID),
		PlatformThreadID: req.ThreadID, ReplyTo: cmp.Or(req.MessageID, req.ReplyTo),
		DeliveryID: req.DeliveryID, RequesterID: req.TargetID,
		LifecycleFeedback: req.LifecycleFeedback, Stream: req.Stream,
	})
}
