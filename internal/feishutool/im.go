package feishutool

import (
	"context"
	"encoding/json"
	"fmt"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/vaayne/anna/internal/toolspec"
)

var imInputSchema = func() map[string]any {
	var m map[string]any
	_ = json.Unmarshal([]byte(`{
  "type": "object",
  "properties": {
    "action": {
      "type": "string",
      "enum": ["send_message", "reply_message", "read_messages", "get_message", "forward_message", "add_reaction", "remove_reaction"],
      "description": "The action to perform"
    },
    "receive_id_type": {
      "type": "string",
      "enum": ["open_id", "chat_id"],
      "description": "Receiver ID type for send_message: open_id (DM, ou_xxx) or chat_id (group, oc_xxx)"
    },
    "receive_id": {
      "type": "string",
      "description": "Receiver ID for send_message. Must match receive_id_type."
    },
    "message_id": {
      "type": "string",
      "description": "Message ID (om_xxx). Required for reply_message, get_message, forward_message, add_reaction, remove_reaction."
    },
    "msg_type": {
      "type": "string",
      "enum": ["text", "post", "interactive", "image", "file", "share_chat", "share_user"],
      "description": "Message type for send/reply. Most common: text."
    },
    "content": {
      "type": "string",
      "description": "Message content as JSON string. Format depends on msg_type. text: '{\"text\":\"hello\"}', post: '{\"zh_cn\":{\"title\":\"Title\",\"content\":[[{\"tag\":\"text\",\"text\":\"body\"}]]}}'"
    },
    "chat_id": {
      "type": "string",
      "description": "Chat ID for read_messages (oc_xxx) or forward_message target"
    },
    "container_id": {
      "type": "string",
      "description": "Container ID for read_messages (typically chat_id)"
    },
    "start_time": {
      "type": "string",
      "description": "Start time for read_messages (Unix seconds string)"
    },
    "end_time": {
      "type": "string",
      "description": "End time for read_messages (Unix seconds string)"
    },
    "sort_type": {
      "type": "string",
      "enum": ["ByCreateTimeAsc", "ByCreateTimeDesc"],
      "description": "Sort order for read_messages (default: ByCreateTimeAsc)"
    },
    "reaction_type": {
      "type": "string",
      "description": "Emoji type for add_reaction/remove_reaction, e.g. 'THUMBSUP', 'SMILE', 'HEART'"
    },
    "reaction_id": {
      "type": "string",
      "description": "Reaction ID for remove_reaction (from reaction list)"
    },
    "uuid": {
      "type": "string",
      "description": "Idempotent UUID for send/reply. Same UUID within 1 hour deduplicates."
    },
    "page_size": {
      "type": "number",
      "description": "Page size for read_messages (default 20, max 50)"
    },
    "page_token": {
      "type": "string",
      "description": "Pagination token"
    }
  },
  "required": ["action"]
}`), &m)
	return m
}()

// IMTool provides Feishu instant messaging operations.
type IMTool struct {
	client *Client
}

// NewIMTool creates a feishu_im tool.
func NewIMTool(client *Client) *IMTool {
	return &IMTool{client: client}
}

func (t *IMTool) Definition() toolspec.Definition {
	return toolspec.Definition{
		Name: "feishu_im",
		Description: `Send and read Feishu/Lark IM messages, manage reactions. Uses user token when available.

IMPORTANT: This tool sends messages as the authorized user. Always confirm with the user before sending: 1) who to send to, 2) message content.

Actions:
- send_message: Send a message to a user (DM) or group chat. Requires receive_id_type, receive_id, msg_type, content.
- reply_message: Reply to a specific message. Requires message_id, msg_type, content.
- read_messages: Read message history from a chat. Requires container_id (chat_id). Optional: start_time, end_time, sort_type, page_size, page_token.
- get_message: Get a single message by ID. Requires message_id.
- forward_message: Forward a message to another chat. Requires message_id and receive_id (target chat_id).
- add_reaction: Add an emoji reaction to a message. Requires message_id and reaction_type (e.g. 'THUMBSUP').
- remove_reaction: Remove a reaction. Requires message_id and reaction_id.

Content format (JSON string): text -> '{"text":"hello"}', post -> '{"zh_cn":{"title":"T","content":[[{"tag":"text","text":"body"}]]}}', interactive -> card JSON.`,
		InputSchema: imInputSchema,
	}
}

func (t *IMTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	action := stringArg(args, "action")
	switch action {
	case "send_message":
		return t.sendMessage(ctx, args)
	case "reply_message":
		return t.replyMessage(ctx, args)
	case "read_messages":
		return t.readMessages(ctx, args)
	case "get_message":
		return t.getMessage(ctx, args)
	case "forward_message":
		return t.forwardMessage(ctx, args)
	case "add_reaction":
		return t.addReaction(ctx, args)
	case "remove_reaction":
		return t.removeReaction(ctx, args)
	default:
		return "", fmt.Errorf("feishu_im: unknown action %q", action)
	}
}

func (t *IMTool) sendMessage(ctx context.Context, args map[string]any) (string, error) {
	recvIDType := stringArg(args, "receive_id_type")
	recvID := stringArg(args, "receive_id")
	msgType := stringArg(args, "msg_type")
	content := stringArg(args, "content")
	if recvIDType == "" || recvID == "" {
		return "", fmt.Errorf("feishu_im send_message: receive_id_type and receive_id are required")
	}
	if msgType == "" || content == "" {
		return "", fmt.Errorf("feishu_im send_message: msg_type and content are required")
	}

	var result any
	invokeErr := t.client.InvokeWithUserToken(ctx, false, func(ctx context.Context, opts ...larkcore.RequestOptionFunc) error {
		bodyBuilder := larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(recvID).
			MsgType(msgType).
			Content(content)
		if uuid := stringArg(args, "uuid"); uuid != "" {
			bodyBuilder.Uuid(uuid)
		}

		resp, err := t.client.Lark().Im.Message.Create(ctx,
			larkim.NewCreateMessageReqBuilder().
				ReceiveIdType(recvIDType).
				Body(bodyBuilder.Build()).
				Build(),
			opts...)
		if err != nil {
			return fmt.Errorf("send message: %w", err)
		}
		if !resp.Success() {
			return fmt.Errorf("send message: %s", FormatLarkError(resp.Code, resp.Msg))
		}
		result = map[string]any{"message": resp.Data}
		return nil
	})
	if invokeErr != nil {
		return "", fmt.Errorf("feishu_im send_message: %w", invokeErr)
	}
	return JSONResultFromAny(result)
}

func (t *IMTool) replyMessage(ctx context.Context, args map[string]any) (string, error) {
	messageID := stringArg(args, "message_id")
	if messageID == "" {
		messageID = MessageIDFromContext(ctx)
	}
	if messageID == "" {
		return "", fmt.Errorf("feishu_im reply_message: message_id is required")
	}
	msgType := stringArg(args, "msg_type")
	content := stringArg(args, "content")
	if msgType == "" || content == "" {
		return "", fmt.Errorf("feishu_im reply_message: msg_type and content are required")
	}

	var result any
	invokeErr := t.client.InvokeWithUserToken(ctx, false, func(ctx context.Context, opts ...larkcore.RequestOptionFunc) error {
		bodyBuilder := larkim.NewReplyMessageReqBodyBuilder().
			MsgType(msgType).
			Content(content)
		if uuid := stringArg(args, "uuid"); uuid != "" {
			bodyBuilder.Uuid(uuid)
		}

		resp, err := t.client.Lark().Im.Message.Reply(ctx,
			larkim.NewReplyMessageReqBuilder().
				MessageId(messageID).
				Body(bodyBuilder.Build()).
				Build(),
			opts...)
		if err != nil {
			return fmt.Errorf("reply message: %w", err)
		}
		if !resp.Success() {
			return fmt.Errorf("reply message: %s", FormatLarkError(resp.Code, resp.Msg))
		}
		result = map[string]any{"message": resp.Data}
		return nil
	})
	if invokeErr != nil {
		return "", fmt.Errorf("feishu_im reply_message: %w", invokeErr)
	}
	return JSONResultFromAny(result)
}

func (t *IMTool) readMessages(ctx context.Context, args map[string]any) (string, error) {
	containerID := stringArg(args, "container_id")
	if containerID == "" {
		containerID = stringArg(args, "chat_id")
	}
	if containerID == "" {
		containerID = ChatIDFromContext(ctx)
	}
	if containerID == "" {
		return "", fmt.Errorf("feishu_im read_messages: container_id or chat_id is required")
	}

	var result map[string]any
	invokeErr := t.client.InvokeWithUserToken(ctx, false, func(ctx context.Context, opts ...larkcore.RequestOptionFunc) error {
		builder := larkim.NewListMessageReqBuilder().
			ContainerIdType("chat").
			ContainerId(containerID)
		if st := stringArg(args, "start_time"); st != "" {
			builder.StartTime(st)
		}
		if et := stringArg(args, "end_time"); et != "" {
			builder.EndTime(et)
		}
		if sort := stringArg(args, "sort_type"); sort != "" {
			builder.SortType(sort)
		}
		if ps := intArg(args, "page_size"); ps > 0 {
			builder.PageSize(ps)
		}
		if pt := stringArg(args, "page_token"); pt != "" {
			builder.PageToken(pt)
		}

		resp, err := t.client.Lark().Im.Message.List(ctx, builder.Build(), opts...)
		if err != nil {
			return fmt.Errorf("read messages: %w", err)
		}
		if !resp.Success() {
			return fmt.Errorf("read messages: %s", FormatLarkError(resp.Code, resp.Msg))
		}

		hasMore := resp.Data.HasMore != nil && *resp.Data.HasMore
		pt := ""
		if resp.Data.PageToken != nil {
			pt = *resp.Data.PageToken
		}
		result = map[string]any{
			"messages":   resp.Data.Items,
			"has_more":   hasMore,
			"page_token": pt,
		}
		return nil
	})
	if invokeErr != nil {
		return "", fmt.Errorf("feishu_im read_messages: %w", invokeErr)
	}
	return JSONResultFromAny(result)
}

func (t *IMTool) getMessage(ctx context.Context, args map[string]any) (string, error) {
	messageID := stringArg(args, "message_id")
	if messageID == "" {
		messageID = MessageIDFromContext(ctx)
	}
	if messageID == "" {
		return "", fmt.Errorf("feishu_im get_message: message_id is required")
	}

	var result any
	invokeErr := t.client.InvokeWithUserToken(ctx, false, func(ctx context.Context, opts ...larkcore.RequestOptionFunc) error {
		resp, err := t.client.Lark().Im.Message.Get(ctx,
			larkim.NewGetMessageReqBuilder().
				MessageId(messageID).
				Build(),
			opts...)
		if err != nil {
			return fmt.Errorf("get message: %w", err)
		}
		if !resp.Success() {
			return fmt.Errorf("get message: %s", FormatLarkError(resp.Code, resp.Msg))
		}
		result = map[string]any{"message": resp.Data}
		return nil
	})
	if invokeErr != nil {
		return "", fmt.Errorf("feishu_im get_message: %w", invokeErr)
	}
	return JSONResultFromAny(result)
}

func (t *IMTool) forwardMessage(ctx context.Context, args map[string]any) (string, error) {
	messageID := stringArg(args, "message_id")
	if messageID == "" {
		return "", fmt.Errorf("feishu_im forward_message: message_id is required")
	}
	recvID := stringArg(args, "receive_id")
	if recvID == "" {
		recvID = stringArg(args, "chat_id")
	}
	if recvID == "" {
		return "", fmt.Errorf("feishu_im forward_message: receive_id or chat_id is required")
	}

	var result any
	invokeErr := t.client.InvokeWithUserToken(ctx, false, func(ctx context.Context, opts ...larkcore.RequestOptionFunc) error {
		resp, err := t.client.Lark().Im.Message.Forward(ctx,
			larkim.NewForwardMessageReqBuilder().
				MessageId(messageID).
				ReceiveIdType("chat_id").
				Body(larkim.NewForwardMessageReqBodyBuilder().
					ReceiveId(recvID).
					Build()).
				Build(),
			opts...)
		if err != nil {
			return fmt.Errorf("forward message: %w", err)
		}
		if !resp.Success() {
			return fmt.Errorf("forward message: %s", FormatLarkError(resp.Code, resp.Msg))
		}
		result = map[string]any{"message": resp.Data}
		return nil
	})
	if invokeErr != nil {
		return "", fmt.Errorf("feishu_im forward_message: %w", invokeErr)
	}
	return JSONResultFromAny(result)
}

func (t *IMTool) addReaction(ctx context.Context, args map[string]any) (string, error) {
	messageID := stringArg(args, "message_id")
	if messageID == "" {
		messageID = MessageIDFromContext(ctx)
	}
	if messageID == "" {
		return "", fmt.Errorf("feishu_im add_reaction: message_id is required")
	}
	reactionType := stringArg(args, "reaction_type")
	if reactionType == "" {
		return "", fmt.Errorf("feishu_im add_reaction: reaction_type is required")
	}

	var result any
	invokeErr := t.client.InvokeWithUserToken(ctx, false, func(ctx context.Context, opts ...larkcore.RequestOptionFunc) error {
		resp, err := t.client.Lark().Im.MessageReaction.Create(ctx,
			larkim.NewCreateMessageReactionReqBuilder().
				MessageId(messageID).
				Body(larkim.NewCreateMessageReactionReqBodyBuilder().
					ReactionType(larkim.NewEmojiBuilder().EmojiType(reactionType).Build()).
					Build()).
				Build(),
			opts...)
		if err != nil {
			return fmt.Errorf("add reaction: %w", err)
		}
		if !resp.Success() {
			return fmt.Errorf("add reaction: %s", FormatLarkError(resp.Code, resp.Msg))
		}
		result = map[string]any{"reaction": resp.Data}
		return nil
	})
	if invokeErr != nil {
		return "", fmt.Errorf("feishu_im add_reaction: %w", invokeErr)
	}
	return JSONResultFromAny(result)
}

func (t *IMTool) removeReaction(ctx context.Context, args map[string]any) (string, error) {
	messageID := stringArg(args, "message_id")
	if messageID == "" {
		messageID = MessageIDFromContext(ctx)
	}
	if messageID == "" {
		return "", fmt.Errorf("feishu_im remove_reaction: message_id is required")
	}
	reactionID := stringArg(args, "reaction_id")
	if reactionID == "" {
		return "", fmt.Errorf("feishu_im remove_reaction: reaction_id is required")
	}

	invokeErr := t.client.InvokeWithUserToken(ctx, false, func(ctx context.Context, opts ...larkcore.RequestOptionFunc) error {
		resp, err := t.client.Lark().Im.MessageReaction.Delete(ctx,
			larkim.NewDeleteMessageReactionReqBuilder().
				MessageId(messageID).
				ReactionId(reactionID).
				Build(),
			opts...)
		if err != nil {
			return fmt.Errorf("remove reaction: %w", err)
		}
		if !resp.Success() {
			return fmt.Errorf("remove reaction: %s", FormatLarkError(resp.Code, resp.Msg))
		}
		return nil
	})
	if invokeErr != nil {
		return "", fmt.Errorf("feishu_im remove_reaction: %w", invokeErr)
	}
	return JSONResultFromAny(map[string]any{"success": true})
}
