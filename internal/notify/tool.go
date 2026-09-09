package notify

import (
	"context"
	"fmt"

	"github.com/CherryHQ/stella/internal/agentrun"
	"github.com/CherryHQ/stella/internal/authz"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	pkgtools "github.com/CherryHQ/stella/pkg/tools"
)

// NewTool creates a notify tool with the given notifier service.
// Returns nil if notifier is nil.
func NewTool(notifier pkgplugins.Notifier) pkgtools.Tool {
	if notifier == nil {
		return nil
	}
	return &notifyTool{service: notifier}
}

type notifyTool struct {
	service pkgplugins.Notifier
}

var notifyInputSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"message": map[string]any{
			"type":        "string",
			"description": "The notification message to send (supports markdown). For Feishu channels, use {{button value=\"action_id\" type=\"primary\" label=\"Button Text\"}} to render interactive card buttons (must NOT be inside code blocks). Supported button attributes: value (required), label (required), type (\"default\"|\"primary\"|\"danger\"), confirm (optional confirmation text).",
		},
		"channel": map[string]any{
			"type":        "string",
			"description": "Target backend (one of \"telegram\", \"discord\", \"feishu\", \"dingtalk\", \"qq\", \"weixin\"). Omit to broadcast to all configured backends.",
		},
		"chat_id": map[string]any{
			"type":        "string",
			"description": "Target chat/channel within the backend. Usually omit this in user conversations so Stella can resolve the linked identity automatically. Never invent IDs. For Feishu, use a union_id (on_...) for a person; use a chat_id (oc_...) only when it was explicitly provided or came from the current message context and the bot is in that chat.",
		},
		"silent": map[string]any{
			"type":        "boolean",
			"description": "Send without notification sound. Honored by Telegram and Discord; ignored by other backends.",
		},
	},
	"required": []string{"message"},
}

func (t *notifyTool) Definition() pkgtools.Definition {
	return pkgtools.Definition{
		Name:        "notify",
		Description: "Send a notification message to the user. In normal user conversations, omit 'channel' and 'chat_id' so Stella routes via the current user's linked identities automatically. Supported backends: telegram, discord, feishu, dingtalk, qq, weixin. To target a specific recipient, set 'channel' to the backend and 'chat_id' to a real backend target ID; never guess or fabricate IDs. For Feishu person targets, pass a union_id 'on_...' resolved through the same app's directory. Feishu open_id 'ou_...' is app-scoped and may fail; Feishu chat_id 'oc_...' is only for a real chat where the bot is already a member. DingTalk notifications require a still-valid session Webhook learned from a recent inbound message. Use this for proactive messages, alerts, scheduler summaries, or long-running task results.",
		InputSchema: notifyInputSchema,
	}
}

func (t *notifyTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	message, _ := args["message"].(string)
	if message == "" {
		return "", fmt.Errorf("message is required")
	}

	ch, _ := args["channel"].(string)
	chatID, _ := args["chat_id"].(string)
	silent, _ := args["silent"].(bool)

	notification := pkgchannel.Notification{
		Channel: ch,
		ChatID:  chatID,
		AgentID: pkgchannel.NotificationAgentIDFromContext(ctx),
		Text:    message,
		Silent:  silent,
	}
	// Notification delivery is an external side effect. Check the durable Run
	// owner immediately before the one permitted publish attempt.
	if err := agentrun.Check(ctx); err != nil {
		return "", err
	}

	var err error
	if ch == "" && chatID == "" {
		if userID := authz.UserIDFromContext(ctx); userID != "" {
			err = t.service.NotifyUser(ctx, userID, notification)
		} else {
			err = t.service.Notify(ctx, notification)
		}
	} else {
		err = t.service.Notify(ctx, notification)
	}
	if err != nil {
		return "", fmt.Errorf("send notification: %w", err)
	}

	if ch != "" {
		return fmt.Sprintf("Notification sent to %s.", ch), nil
	}
	return "Notification sent.", nil
}
