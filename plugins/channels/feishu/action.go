package feishu

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"

	"github.com/CherryHQ/stella/pkg/channel"
)

// onCardAction handles Feishu card action callbacks (button clicks, etc.).
// It forwards the action to the agent as a synthetic text message.
var cardActionPattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,64}$`)

func validCardAction(action string) bool {
	return cardActionPattern.MatchString(action)
}

func (b *Bot) onCardAction(ctx context.Context, event *callback.CardActionTriggerEvent) (*callback.CardActionTriggerResponse, error) {
	logger().Debug("card action callback received")
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
	botID, _ := b.botOpenID.Load().(string)
	if botID == "" {
		logger().Warn("rejecting Feishu card action: bot open_id is unknown")
		return nil, nil
	}
	if openID == botID {
		return nil, nil
	}

	contextChatID := ""
	messageID := ""
	if req.Context != nil {
		contextChatID = req.Context.OpenChatID
		messageID = req.Context.OpenMessageID
	}

	chatID := ""
	chatType := ""
	rootID := ""
	botAuthored := false
	contextOK := false
	if messageID != "" {
		chatID, chatType, rootID, botAuthored, contextOK = b.resolveMessageContext(messageID)
	}
	if !contextOK || !botAuthored || (contextChatID != "" && contextChatID != chatID) || !b.admitIngress(chatID, chatType, true, false) {
		return nil, nil
	}

	// Extract the action label for the agent message.
	action, _ := req.Action.Value["action"].(string)
	if strings.HasPrefix(action, cancelCardActionPrefix) {
		return b.handleCancelCardAction(ctx, openID, chatID, rootID, action), nil
	}
	if !validCardAction(action) {
		action = "unknown"
	}

	// Build synthetic message text.
	text := fmt.Sprintf("[User clicked: %s]", action)

	unionID := b.resolveUnionID(ctx, openID)
	senderIDs := feishuSenderIDs(unionID, openID)
	msg := b.incomingMsg(senderIDs, chatID, chatType, channel.TextContent(text))
	if unionID == "" && chatType == "p2p" {
		// Keep open_id as a legacy linked-identity candidate, but leave the
		// canonical sender empty so this callback cannot mint a second guest.
		msg.SenderID = ""
	}
	msg.ThreadID = rootID

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

func (b *Bot) handleCancelCardAction(ctx context.Context, openID, chatID, rootID, action string) *callback.CardActionTriggerResponse {
	if b.cancels == nil {
		return cancelToast("This response has already ended.")
	}
	token := cancelActionToken(action)
	entry, ok := b.cancels.get(token)
	if !ok {
		return cancelToast("This response has already ended.")
	}
	requesterIDs := feishuSenderIDs(b.resolveUnionID(ctx, openID), openID)
	if entry.requesterID == "" || !containsFeishuID(requesterIDs, entry.requesterID) {
		return cancelToast("Only the requester can cancel this response.")
	}
	if entry.chatID != chatID || entry.rootID != rootID {
		return cancelToast("This action is not available here.")
	}
	entry, ok = b.cancels.take(token)
	if !ok || entry.control == nil || !entry.control.abort() {
		return cancelToast("This response has already ended.")
	}
	entry.control.cancelled.Store(true)
	return cancelToast("Stopping…")
}

func cancelToast(content string) *callback.CardActionTriggerResponse {
	return &callback.CardActionTriggerResponse{Toast: &callback.Toast{Type: "info", Content: content}}
}

func containsFeishuID(ids []string, target string) bool {
	return slices.Contains(ids, target)
}
