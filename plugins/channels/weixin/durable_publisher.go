package weixin

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/CherryHQ/stella/pkg/channel"
)

// This bounds Stella's reconstruction window, not the provider's validity guarantee.
// iLink may invalidate a context token earlier; a failed send is never replayed.
const durableReplyMaxAge = 24 * time.Hour

type durableReplySecret struct {
	UserID       string `json:"user_id"`
	ContextToken string `json:"context_token"`
}

type durableIncomingPublisher struct {
	bot     *Bot
	userID  string
	expires time.Time
}

func durableReplyCapability(msg WeixinMessage) *channel.ReplyCapability {
	if msg.ContextToken == "" || msg.FromUserID == "" {
		return nil
	}
	secret, _ := json.Marshal(durableReplySecret{UserID: msg.FromUserID, ContextToken: msg.ContextToken})
	return &channel.ReplyCapability{
		Kind: "weixin_context_token", Secret: string(secret),
		ExpiresAt: time.Now().UTC().Add(durableReplyMaxAge),
	}
}

// NewDurablePublisher builds HTTP egress from a channel-bound encrypted
// capability, without polling or looking up a listener-local context token.
func NewDurablePublisher(cfg Config, secret string, expires time.Time) (channel.DurablePublisher, error) {
	if !time.Now().UTC().Before(expires) {
		return nil, fmt.Errorf("weixin: durable reply capability expired")
	}
	var reply durableReplySecret
	if err := json.Unmarshal([]byte(secret), &reply); err != nil || reply.UserID == "" || reply.ContextToken == "" {
		return nil, fmt.Errorf("weixin: invalid durable reply capability")
	}
	b, err := New(cfg, nil)
	if err != nil {
		return nil, err
	}
	b.client = NewClient(cfg.BaseURL, "", cfg.BotToken, cfg.SKRouteTag, cfg.Version)
	b.contextTokens.Store(reply.UserID, reply.ContextToken)
	return &durableIncomingPublisher{bot: b, userID: reply.UserID, expires: expires.UTC()}, nil
}

func (p *durableIncomingPublisher) PublishIncoming(ctx context.Context, req channel.DurablePublishRequest) (err error) {
	if req.Stream == nil {
		return nil
	}
	defer req.Stream.Discard()
	outcome := channel.EgressDiscarded
	defer func() {
		ackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if ackErr := req.Stream.Ack(ackCtx, outcome); ackErr != nil && err == nil {
			err = ackErr
		}
	}()
	if req.IsGroup || cmp.Or(req.TargetID, req.ChatID) != p.userID {
		return fmt.Errorf("weixin: durable reply recipient does not match capability")
	}
	if !time.Now().UTC().Before(p.expires) {
		return fmt.Errorf("weixin: durable reply capability expired")
	}
	stream, err := channel.ValidateGroupReplay(ctx, req.Stream)
	if err != nil {
		return err
	}
	response, tracker, images, files, err := p.bot.streamEventsChecked(stream)
	if err != nil {
		return err
	}
	if strings.TrimSpace(response) == "" {
		response = "(empty response)"
	}
	if tracker != nil && tracker.HasHistory() {
		response += tracker.RenderFinal()
	}
	if !time.Now().UTC().Before(p.expires) {
		return fmt.Errorf("weixin: durable reply capability expired before delivery")
	}
	if err := stream.CheckOperation(ctx); err != nil {
		return err
	}
	outcome = channel.EgressUnknown
	if err := p.bot.sendFinalResponseChecked(ctx, stream, WeixinMessage{FromUserID: p.userID}, response, images, files); err != nil {
		return err
	}
	outcome = channel.EgressDelivered
	return nil
}
