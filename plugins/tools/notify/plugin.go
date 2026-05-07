package notify

import (
	"context"
	"fmt"

	pkgchannel "github.com/vaayne/anna/pkg/channel"
	"github.com/vaayne/anna/pkg/memory"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
	"github.com/vaayne/anna/pkg/tools"
)

const PluginID = "tool/notify"

func init() {
	pkgplugins.Register(PluginID, pkgplugins.PluginFunc(func(host pkgplugins.Host) {
		host.SetInfo(pkgplugins.PluginInfo{
			ID:          PluginID,
			Kind:        "tool",
			Name:        "notify",
			DisplayName: "Notify",
			Description: "Send notifications through Anna's configured notification routes.",
			Capabilities: []string{
				pkgplugins.CapabilityTool,
			},
		})
		host.AddTool(pkgplugins.ToolSpec{
			PluginID:    PluginID,
			Name:        "notify",
			Description: "Send a notification message to the user.",
			Required:    true,
			Build: func(ctx pkgplugins.ToolContext) (tools.Tool, error) {
				service := ctx.Platform.Notifier()
				if service == nil {
					return nil, nil
				}
				return &Tool{service: service}, nil
			},
		})
	}))
}

// Tool is an agent tool that sends notifications through the plugin host.
type Tool struct {
	service pkgplugins.Notifier
}

var inputSchema = map[string]any{
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
			"description": "Target chat/channel within the backend. Usually omit this in user conversations so Anna can resolve the linked identity automatically.",
		},
		"silent": map[string]any{
			"type":        "boolean",
			"description": "Send without notification sound",
		},
	},
	"required": []string{"message"},
}

func (t *Tool) Definition() tools.Definition {
	return tools.Definition{
		Name:        "notify",
		Description: "Send a notification message to the user. In normal user conversations, omit 'chat_id' so Anna can route via the current user's linked identities automatically. Supports multiple backends (Telegram, Slack, etc.). Use this for proactive messages, alerts, scheduler summaries, or long-running task results.",
		InputSchema: inputSchema,
	}
}

func (t *Tool) Execute(ctx context.Context, args map[string]any) (string, error) {
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

	// Auto-fill reply targeting from context when not explicitly provided.
	if rc, ok := pkgchannel.NotificationReplyFromContext(ctx); ok {
		if notification.ChatID == "" {
			notification.ChatID = rc.ChatID
		}
		if notification.ReplyToMessageID == "" {
			notification.ReplyToMessageID = rc.MessageID
		}
	}

	var err error
	if ch == "" && chatID == "" {
		if userID := memory.UserIDFromContext(ctx); userID != 0 {
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
