package dingtalk

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/CherryHQ/stella/pkg/channel"
)

type durableGroupPublisher struct {
	session webhookSession
}

func (p durableGroupPublisher) PublishIncoming(ctx context.Context, req channel.DurablePublishRequest) error {
	return p.Publish(ctx, channel.GroupPublishRequest{
		Platform: req.Platform, PlatformGroupID: cmp.Or(req.ChatID, req.TargetID),
		PlatformThreadID: req.ThreadID, ReplyTo: cmp.Or(req.MessageID, req.ReplyTo),
		DeliveryID: req.DeliveryID, RequesterID: req.TargetID,
		LifecycleFeedback: req.LifecycleFeedback, Stream: req.Stream,
	})
}

// NewDurableGroupPublisher receives a decrypted, channel-bound capability
// from the host. It owns no listener or shared session registry.
func NewDurableGroupPublisher(webhook string, expires time.Time) (channel.GroupPublisher, error) {
	if !expires.After(time.Now().UTC()) {
		return nil, fmt.Errorf("dingtalk: reply capability expired")
	}
	u, err := url.Parse(webhook)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return nil, fmt.Errorf("dingtalk: invalid reply capability")
	}
	return durableGroupPublisher{session: webhookSession{URL: webhook, ExpiresAt: expires.UTC()}}, nil
}

func (p durableGroupPublisher) Publish(ctx context.Context, req channel.GroupPublishRequest) error {
	// Reuse the normal renderer and completion handshake. This single-request
	// map comes entirely from the durable envelope and decrypted capability.
	b := &Bot{
		groupSessions: map[string]webhookSession{strings.TrimPrefix(req.PlatformGroupID, "dingtalk:"): p.session},
		replyToWebhook: func(ctx context.Context, webhook, text string) error {
			if !time.Now().UTC().Before(p.session.ExpiresAt) {
				return fmt.Errorf("dingtalk: reply capability expired")
			}
			if err := sendWebhookText(ctx, webhook, text); err != nil {
				// HTTP errors and provider bodies can echo the credential URL.
				// Durable failure records must never retain that plaintext.
				return errors.New("dingtalk: durable webhook delivery failed")
			}
			return nil
		},
	}
	return b.Publish(ctx, req)
}
