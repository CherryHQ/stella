package feishutool

import (
	"context"
	"encoding/json"
	"fmt"

	larkcontact "github.com/larksuite/oapi-sdk-go/v3/service/contact/v3"
	"github.com/vaayne/anna/internal/toolspec"
)

var userInputSchema = func() map[string]any {
	var m map[string]any
	_ = json.Unmarshal([]byte(`{
  "type": "object",
  "properties": {
    "action": {
      "type": "string",
      "enum": ["get_user", "search_user"],
      "description": "The action to perform"
    },
    "open_id": {
      "type": "string",
      "description": "The user's open_id (required for get_user)"
    },
    "emails": {
      "type": "array",
      "items": {"type": "string"},
      "description": "Email addresses to search (for search_user, max 50)"
    },
    "mobiles": {
      "type": "array",
      "items": {"type": "string"},
      "description": "Mobile numbers to search (for search_user, max 50). Non-China numbers need '+' country code prefix"
    }
  },
  "required": ["action"]
}`), &m)
	return m
}()

// UserTool provides Feishu user lookup capabilities.
// Actions: get_user (by open_id), search_user (by email/mobile).
type UserTool struct {
	client *Client
}

// NewUserTool creates a feishu_user tool.
func NewUserTool(client *Client) *UserTool {
	return &UserTool{client: client}
}

func (t *UserTool) Definition() toolspec.Definition {
	return toolspec.Definition{
		Name: "feishu_user",
		Description: `Look up Feishu/Lark users.

Actions:
- get_user: Get a user's profile by open_id. Returns name, email, department, avatar, status, etc.
- search_user: Find users by email or mobile number. Returns matching user IDs. Useful for resolving a person's identity before other Feishu operations.`,
		InputSchema: userInputSchema,
	}
}

func (t *UserTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	action := stringArg(args, "action")
	switch action {
	case "get_user":
		return t.getUser(ctx, args)
	case "search_user":
		return t.searchUser(ctx, args)
	default:
		return "", fmt.Errorf("feishu_user: unknown action %q, expected get_user/search_user", action)
	}
}

func (t *UserTool) getUser(ctx context.Context, args map[string]any) (string, error) {
	openID := stringArg(args, "open_id")
	if openID == "" {
		// Fall back to context open_id.
		openID = OpenIDFromContext(ctx)
	}
	if openID == "" {
		return "", fmt.Errorf("feishu_user get_user: open_id is required")
	}

	if err := t.client.Wait(ctx); err != nil {
		return "", err
	}

	resp, err := t.client.Lark().Contact.User.Get(ctx,
		larkcontact.NewGetUserReqBuilder().
			UserId(openID).
			UserIdType("open_id").
			Build())
	if err != nil {
		return "", fmt.Errorf("feishu_user get_user: %w", err)
	}
	if !resp.Success() {
		return "", fmt.Errorf("feishu_user get_user: %s", FormatLarkError(resp.Code, resp.Msg))
	}

	if resp.Data == nil || resp.Data.User == nil {
		return "User not found.", nil
	}

	return JSONResultFromAny(formatUser(resp.Data.User))
}

func (t *UserTool) searchUser(ctx context.Context, args map[string]any) (string, error) {
	emails := toStringSlice(args, "emails")
	mobiles := toStringSlice(args, "mobiles")

	if len(emails) == 0 && len(mobiles) == 0 {
		return "", fmt.Errorf("feishu_user search_user: at least one of emails or mobiles is required")
	}

	if err := t.client.Wait(ctx); err != nil {
		return "", err
	}

	bodyBuilder := larkcontact.NewBatchGetIdUserReqBodyBuilder()
	if len(emails) > 0 {
		bodyBuilder.Emails(emails)
	}
	if len(mobiles) > 0 {
		bodyBuilder.Mobiles(mobiles)
	}

	resp, err := t.client.Lark().Contact.User.BatchGetId(ctx,
		larkcontact.NewBatchGetIdUserReqBuilder().
			UserIdType("open_id").
			Body(bodyBuilder.Build()).
			Build())
	if err != nil {
		return "", fmt.Errorf("feishu_user search_user: %w", err)
	}
	if !resp.Success() {
		return "", fmt.Errorf("feishu_user search_user: %s", FormatLarkError(resp.Code, resp.Msg))
	}

	if resp.Data == nil || len(resp.Data.UserList) == 0 {
		return "No users found.", nil
	}

	results := make([]map[string]any, 0, len(resp.Data.UserList))
	for _, u := range resp.Data.UserList {
		m := map[string]any{}
		if u.UserId != nil {
			m["open_id"] = *u.UserId
		}
		if u.Email != nil {
			m["email"] = *u.Email
		}
		if u.Mobile != nil {
			m["mobile"] = *u.Mobile
		}
		results = append(results, m)
	}

	return JSONResultFromAny(results)
}

// formatUser extracts key fields from a Lark User into a clean map.
func formatUser(u *larkcontact.User) map[string]any {
	m := map[string]any{}
	if v := derefStr(u.OpenId); v != "" {
		m["open_id"] = v
	}
	if v := derefStr(u.UserId); v != "" {
		m["user_id"] = v
	}
	if v := derefStr(u.Name); v != "" {
		m["name"] = v
	}
	if v := derefStr(u.EnName); v != "" {
		m["en_name"] = v
	}
	if v := derefStr(u.Nickname); v != "" {
		m["nickname"] = v
	}
	if v := derefStr(u.Email); v != "" {
		m["email"] = v
	}
	if v := derefStr(u.Mobile); v != "" {
		m["mobile"] = v
	}
	if u.Avatar != nil {
		if v := derefStr(u.Avatar.AvatarOrigin); v != "" {
			m["avatar"] = v
		}
	}
	if u.Status != nil {
		status := map[string]any{}
		if u.Status.IsFrozen != nil {
			status["is_frozen"] = *u.Status.IsFrozen
		}
		if u.Status.IsResigned != nil {
			status["is_resigned"] = *u.Status.IsResigned
		}
		if u.Status.IsActivated != nil {
			status["is_activated"] = *u.Status.IsActivated
		}
		m["status"] = status
	}
	if v := derefStr(u.Description); v != "" {
		m["description"] = v
	}
	if v := derefStr(u.City); v != "" {
		m["city"] = v
	}
	if v := derefStr(u.Country); v != "" {
		m["country"] = v
	}
	if v := derefInt(u.Gender); v != 0 {
		switch v {
		case 1:
			m["gender"] = "male"
		case 2:
			m["gender"] = "female"
		default:
			m["gender"] = "other"
		}
	}
	return m
}

// toStringSlice extracts a string slice from a tool args map.
func toStringSlice(args map[string]any, key string) []string {
	v, ok := args[key]
	if !ok {
		return nil
	}
	switch s := v.(type) {
	case []any:
		out := make([]string, 0, len(s))
		for _, item := range s {
			if str, ok := item.(string); ok {
				out = append(out, str)
			}
		}
		return out
	case []string:
		return s
	default:
		return nil
	}
}
