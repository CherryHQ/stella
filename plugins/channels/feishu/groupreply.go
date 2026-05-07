package feishu

import (
	"context"
	"fmt"

	"github.com/vaayne/anna/pkg/tools"
)

type groupReplyFnKey struct{}

// WithGroupReplyFn returns a new context carrying the group reply callback.
func WithGroupReplyFn(ctx context.Context, fn func(string)) context.Context {
	return context.WithValue(ctx, groupReplyFnKey{}, fn)
}

func GroupReplyFnFromCtx(ctx context.Context) func(string) {
	fn, _ := ctx.Value(groupReplyFnKey{}).(func(string))
	return fn
}

// GroupReplyTool allows the agent to send a message to a group chat.
// The reply function is injected via context so one tool instance serves all sessions.
type GroupReplyTool struct{}

func (t *GroupReplyTool) Definition() tools.Definition {
	return tools.Definition{
		Name:        "group_reply",
		Description: "Send a message to the group chat. Only use this when you have something useful to contribute to the conversation.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"text": map[string]any{
					"type":        "string",
					"description": "The message to send to the group chat",
				},
			},
			"required": []string{"text"},
		},
	}
}

func (t *GroupReplyTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	text, _ := args["text"].(string)
	if text == "" {
		return "", fmt.Errorf("text is required")
	}

	replyFn := GroupReplyFnFromCtx(ctx)
	if replyFn == nil {
		return "", fmt.Errorf("group_reply is only available in group chat context")
	}

	replyFn(text)
	return "Message sent to group.", nil
}
