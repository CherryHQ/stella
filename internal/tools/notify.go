package tools

import (
	"context"
	"fmt"

	"github.com/CherryHQ/stella/internal/memory"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	pkgtools "github.com/CherryHQ/stella/pkg/tools"
)

// NewNotifyTool creates a notify tool with the given notifier service.
// Returns nil if notifier is nil.
func NewNotifyTool(notifier pkgplugins.Notifier) pkgtools.Tool {
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
			"description": "The notification message to send (supports markdown)",
		},
		"channel": map[string]any{
			"type":        "string",
			"description": "Target backend (e.g. \"telegram\", \"slack\"). Omit to broadcast to all configured backends.",
		},
		"chat_id": map[string]any{
			"type":        "string",
			"description": "Target chat/channel within the backend. Usually omit this in user conversations so Stella can resolve the linked identity automatically.",
		},
		"silent": map[string]any{
			"type":        "boolean",
			"description": "Send without notification sound",
		},
	},
	"required": []string{"message"},
}

func (t *notifyTool) Definition() pkgtools.Definition {
	return pkgtools.Definition{
		Name:        "notify",
		Description: "Send a notification message to the user. In normal user conversations, omit 'chat_id' so Stella can route via the current user's linked identities automatically. Supports multiple backends (Telegram, Slack, etc.). Use this for proactive messages, alerts, scheduler summaries, or long-running task results.",
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

	var err error
	if ch == "" && chatID == "" {
		if userID := memory.UserIDFromContext(ctx); userID != "" {
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
