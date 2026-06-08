package feishu

import (
	"context"
	"fmt"
	"strings"

	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"

	"github.com/CherryHQ/stella/pkg/channel"
)

// onCardAction handles Feishu card action callbacks (button clicks, etc.).
// It forwards the action to the agent as a synthetic text message.
func (b *Bot) onCardAction(ctx context.Context, event *callback.CardActionTriggerEvent) (*callback.CardActionTriggerResponse, error) {
	if event == nil || event.Event == nil {
		return nil, nil
	}

	req := event.Event
	if req.Action == nil {
		return nil, nil
	}

	openID := ""
	if req.Operator != nil {
		openID = req.Operator.OpenID
	}
	if openID == "" {
		return nil, nil
	}

	// Ignore actions from the bot itself.
	if botID, _ := b.botOpenID.Load().(string); botID != "" && openID == botID {
		return nil, nil
	}

	chatID := ""
	messageID := ""
	if req.Context != nil {
		chatID = req.Context.OpenChatID
		messageID = req.Context.OpenMessageID
	}

	// Resolve thread context for the card message.
	chatType := "p2p"
	rootID := ""
	if b.client != nil && messageID != "" {
		_, chatType, rootID = b.getMessageContext(messageID)
	} else if strings.HasPrefix(chatID, "oc_") {
		chatType = "group"
	}

	// Extract the action label for the agent message.
	action, _ := req.Action.Value["action"].(string)
	if action == "" {
		action = "unknown"
	}

	// Build synthetic message text.
	text := fmt.Sprintf("[User clicked: %s]", action)

	senderIDs := feishuSenderIDs(openID)
	msg := b.incomingMsg(senderIDs, chatID, chatType, channel.TextContent(text))

	replyFn := func(reply string) {
		replyCtx, cancel := b.apiContext()
		defer cancel()
		b.replyInThread(replyCtx, messageID, rootID, reply)
	}

	go b.handleIncoming(msg, "", "", openID, chatID, messageID, rootID, replyFn)

	return &callback.CardActionTriggerResponse{
		Toast: &callback.Toast{
			Type:    "info",
			Content: "Processing...",
		},
	}, nil
}
