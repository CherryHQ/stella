package feishutool

import (
	"context"
	"encoding/json"
	"fmt"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/vaayne/anna/internal/toolspec"
)

var chatInputSchema = func() map[string]any {
	var m map[string]any
	_ = json.Unmarshal([]byte(`{
  "type": "object",
  "properties": {
    "action": {
      "type": "string",
      "enum": ["search_chats", "get_chat", "list_members", "add_members", "remove_members"],
      "description": "The action to perform"
    },
    "chat_id": {
      "type": "string",
      "description": "Chat/group ID (oc_xxx format). Required for get_chat, list_members, add_members, remove_members."
    },
    "query": {
      "type": "string",
      "description": "Search keyword for search_chats. Matches group name and member names."
    },
    "member_ids": {
      "type": "array",
      "items": {"type": "string"},
      "description": "User open_ids to add or remove (for add_members, remove_members)"
    },
    "page_size": {
      "type": "number",
      "description": "Page size for list operations (default 20)"
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

// ChatTool provides Feishu chat/group management.
type ChatTool struct {
	client *Client
}

// NewChatTool creates a feishu_chat tool.
func NewChatTool(client *Client) *ChatTool {
	return &ChatTool{client: client}
}

func (t *ChatTool) Definition() toolspec.Definition {
	return toolspec.Definition{
		Name: "feishu_chat",
		Description: `Manage Feishu/Lark chats (groups). Uses user token when available.

Actions:
- search_chats: Search for chats visible to the user/bot. Requires query. Returns matching chats with name, description, owner info.
- get_chat: Get detailed info for a specific chat. Requires chat_id (oc_xxx format). Returns name, description, avatar, owner, permissions, member count.
- list_members: List members of a chat. Requires chat_id. Returns paginated member list with open_id and name.
- add_members: Add members to a chat. Requires chat_id and member_ids (array of open_ids).
- remove_members: Remove members from a chat. Requires chat_id and member_ids (array of open_ids).`,
		InputSchema: chatInputSchema,
	}
}

func (t *ChatTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	action := stringArg(args, "action")
	switch action {
	case "search_chats":
		return t.searchChats(ctx, args)
	case "get_chat":
		return t.getChat(ctx, args)
	case "list_members":
		return t.listMembers(ctx, args)
	case "add_members":
		return t.addMembers(ctx, args)
	case "remove_members":
		return t.removeMembers(ctx, args)
	default:
		return "", fmt.Errorf("feishu_chat: unknown action %q", action)
	}
}

func (t *ChatTool) searchChats(ctx context.Context, args map[string]any) (string, error) {
	query := stringArg(args, "query")
	if query == "" {
		return "", fmt.Errorf("feishu_chat search_chats: query is required")
	}

	var result map[string]any
	invokeErr := t.client.InvokeWithUserToken(ctx, false, func(ctx context.Context, opts ...larkcore.RequestOptionFunc) error {
		builder := larkim.NewSearchChatReqBuilder().
			UserIdType("open_id").
			Query(query)
		if ps := intArg(args, "page_size"); ps > 0 {
			builder.PageSize(ps)
		}
		if pt := stringArg(args, "page_token"); pt != "" {
			builder.PageToken(pt)
		}

		resp, err := t.client.Lark().Im.Chat.Search(ctx, builder.Build(), opts...)
		if err != nil {
			return fmt.Errorf("search chats: %w", err)
		}
		if !resp.Success() {
			return fmt.Errorf("search chats: %s", FormatLarkError(resp.Code, resp.Msg))
		}

		hasMore := resp.Data.HasMore != nil && *resp.Data.HasMore
		pt := ""
		if resp.Data.PageToken != nil {
			pt = *resp.Data.PageToken
		}
		result = map[string]any{
			"chats":      resp.Data.Items,
			"has_more":   hasMore,
			"page_token": pt,
		}
		return nil
	})
	if invokeErr != nil {
		return "", fmt.Errorf("feishu_chat search_chats: %w", invokeErr)
	}
	return JSONResultFromAny(result)
}

func (t *ChatTool) getChat(ctx context.Context, args map[string]any) (string, error) {
	chatID := stringArg(args, "chat_id")
	if chatID == "" {
		chatID = ChatIDFromContext(ctx)
	}
	if chatID == "" {
		return "", fmt.Errorf("feishu_chat get_chat: chat_id is required")
	}

	var result any
	invokeErr := t.client.InvokeWithUserToken(ctx, false, func(ctx context.Context, opts ...larkcore.RequestOptionFunc) error {
		resp, err := t.client.Lark().Im.Chat.Get(ctx,
			larkim.NewGetChatReqBuilder().
				ChatId(chatID).
				UserIdType("open_id").
				Build(),
			opts...)
		if err != nil {
			return fmt.Errorf("get chat: %w", err)
		}
		if !resp.Success() {
			return fmt.Errorf("get chat: %s", FormatLarkError(resp.Code, resp.Msg))
		}
		result = map[string]any{"chat": resp.Data}
		return nil
	})
	if invokeErr != nil {
		return "", fmt.Errorf("feishu_chat get_chat: %w", invokeErr)
	}
	return JSONResultFromAny(result)
}

func (t *ChatTool) listMembers(ctx context.Context, args map[string]any) (string, error) {
	chatID := stringArg(args, "chat_id")
	if chatID == "" {
		chatID = ChatIDFromContext(ctx)
	}
	if chatID == "" {
		return "", fmt.Errorf("feishu_chat list_members: chat_id is required")
	}

	var result map[string]any
	invokeErr := t.client.InvokeWithUserToken(ctx, false, func(ctx context.Context, opts ...larkcore.RequestOptionFunc) error {
		builder := larkim.NewGetChatMembersReqBuilder().
			ChatId(chatID).
			MemberIdType("open_id")
		if ps := intArg(args, "page_size"); ps > 0 {
			builder.PageSize(ps)
		}
		if pt := stringArg(args, "page_token"); pt != "" {
			builder.PageToken(pt)
		}

		resp, err := t.client.Lark().Im.ChatMembers.Get(ctx, builder.Build(), opts...)
		if err != nil {
			return fmt.Errorf("list members: %w", err)
		}
		if !resp.Success() {
			return fmt.Errorf("list members: %s", FormatLarkError(resp.Code, resp.Msg))
		}

		hasMore := resp.Data.HasMore != nil && *resp.Data.HasMore
		pt := ""
		if resp.Data.PageToken != nil {
			pt = *resp.Data.PageToken
		}
		result = map[string]any{
			"members":    resp.Data.Items,
			"has_more":   hasMore,
			"page_token": pt,
		}
		return nil
	})
	if invokeErr != nil {
		return "", fmt.Errorf("feishu_chat list_members: %w", invokeErr)
	}
	return JSONResultFromAny(result)
}

func (t *ChatTool) addMembers(ctx context.Context, args map[string]any) (string, error) {
	chatID := stringArg(args, "chat_id")
	if chatID == "" {
		chatID = ChatIDFromContext(ctx)
	}
	if chatID == "" {
		return "", fmt.Errorf("feishu_chat add_members: chat_id is required")
	}
	memberIDs := toStringSlice(args, "member_ids")
	if len(memberIDs) == 0 {
		return "", fmt.Errorf("feishu_chat add_members: member_ids is required")
	}

	invokeErr := t.client.InvokeWithUserToken(ctx, false, func(ctx context.Context, opts ...larkcore.RequestOptionFunc) error {
		resp, err := t.client.Lark().Im.ChatMembers.Create(ctx,
			larkim.NewCreateChatMembersReqBuilder().
				ChatId(chatID).
				MemberIdType("open_id").
				Body(larkim.NewCreateChatMembersReqBodyBuilder().
					IdList(memberIDs).
					Build()).
				Build(),
			opts...)
		if err != nil {
			return fmt.Errorf("add members: %w", err)
		}
		if !resp.Success() {
			return fmt.Errorf("add members: %s", FormatLarkError(resp.Code, resp.Msg))
		}
		return nil
	})
	if invokeErr != nil {
		return "", fmt.Errorf("feishu_chat add_members: %w", invokeErr)
	}
	return JSONResultFromAny(map[string]any{"success": true, "members_added": len(memberIDs)})
}

func (t *ChatTool) removeMembers(ctx context.Context, args map[string]any) (string, error) {
	chatID := stringArg(args, "chat_id")
	if chatID == "" {
		chatID = ChatIDFromContext(ctx)
	}
	if chatID == "" {
		return "", fmt.Errorf("feishu_chat remove_members: chat_id is required")
	}
	memberIDs := toStringSlice(args, "member_ids")
	if len(memberIDs) == 0 {
		return "", fmt.Errorf("feishu_chat remove_members: member_ids is required")
	}

	invokeErr := t.client.InvokeWithUserToken(ctx, false, func(ctx context.Context, opts ...larkcore.RequestOptionFunc) error {
		resp, err := t.client.Lark().Im.ChatMembers.Delete(ctx,
			larkim.NewDeleteChatMembersReqBuilder().
				ChatId(chatID).
				MemberIdType("open_id").
				Body(larkim.NewDeleteChatMembersReqBodyBuilder().
					IdList(memberIDs).
					Build()).
				Build(),
			opts...)
		if err != nil {
			return fmt.Errorf("remove members: %w", err)
		}
		if !resp.Success() {
			return fmt.Errorf("remove members: %s", FormatLarkError(resp.Code, resp.Msg))
		}
		return nil
	})
	if invokeErr != nil {
		return "", fmt.Errorf("feishu_chat remove_members: %w", invokeErr)
	}
	return JSONResultFromAny(map[string]any{"success": true, "members_removed": len(memberIDs)})
}
