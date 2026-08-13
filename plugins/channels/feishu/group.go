package feishu

import (
	"context"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	"github.com/CherryHQ/stella/pkg/channel"
)

// groupSystemPrompt returns the system prompt override for the given chat, if any.
func (b *Bot) groupSystemPrompt(chatID string) string {
	if gc, ok := b.cfg.Groups[chatID]; ok {
		return gc.SystemPrompt
	}
	return ""
}

// groupMemberProvisioner is the subset of the coordinator needed for group
// member management. Feishu type-asserts the handler for this.
type groupMemberProvisioner interface {
	EnsurePlatformGroupMember(ctx context.Context, platform, platformGroupID, channelID string) error
	RemovePlatformGroupMember(ctx context.Context, platform, platformGroupID, channelID string) error
}

func (b *Bot) onBotAdded(_ context.Context, event *larkim.P2ChatMemberBotAddedV1) error {
	if event == nil || event.Event == nil || event.Event.ChatId == nil {
		return nil
	}
	chatID := *event.Event.ChatId
	if !b.chatAllowed(chatID) {
		return nil
	}
	name := derefStr(event.Event.Name)
	logger().Info("bot added to group", "chat_id", chatID, "group_name", name)

	if provisioner, ok := b.handler.(groupMemberProvisioner); ok {
		ctx, cancel := b.apiContext()
		defer cancel()
		if err := provisioner.EnsurePlatformGroupMember(ctx, channel.PlatformFeishu, chatID, b.Name()); err != nil {
			logger().Error("ensure group member on bot_added failed", "chat_id", chatID, "error", err)
		}
	}
	return nil
}

func (b *Bot) onBotDeleted(_ context.Context, event *larkim.P2ChatMemberBotDeletedV1) error {
	if event == nil || event.Event == nil || event.Event.ChatId == nil {
		return nil
	}
	chatID := *event.Event.ChatId
	if !b.chatAllowed(chatID) {
		logger().Warn("ignoring bot_deleted while group chats are disabled", "chat_id", chatID)
		return nil
	}
	name := derefStr(event.Event.Name)
	logger().Info("bot removed from group", "chat_id", chatID, "group_name", name)

	if provisioner, ok := b.handler.(groupMemberProvisioner); ok {
		ctx, cancel := b.apiContext()
		defer cancel()
		if err := provisioner.RemovePlatformGroupMember(ctx, channel.PlatformFeishu, chatID, b.Name()); err != nil {
			logger().Error("remove group member on bot_deleted failed", "chat_id", chatID, "error", err)
		}
	}
	return nil
}

// syncGroups lists all groups the bot is in via the Feishu API and ensures
// group membership for each. Called once at startup.
func (b *Bot) syncGroups() {
	provisioner, ok := b.handler.(groupMemberProvisioner)
	if !ok {
		return
	}

	var pageToken string
	var synced int
	for {
		reqBuilder := larkim.NewListChatReqBuilder()
		if pageToken != "" {
			reqBuilder.PageToken(pageToken)
		}
		ctx, cancel := b.apiContext()
		resp, err := b.listChats(ctx, reqBuilder.Build())
		cancel()
		if err != nil {
			logger().Error("sync groups: list chats failed", "error", err)
			return
		}
		if !resp.Success() {
			logger().Error("sync groups: list chats api error", "code", resp.Code, "msg", resp.Msg)
			return
		}
		for _, chat := range resp.Data.Items {
			if chat.ChatId == nil {
				continue
			}
			chatID := *chat.ChatId
			if !b.chatAllowed(chatID) {
				continue
			}
			ctx, cancel := b.apiContext()
			if err := provisioner.EnsurePlatformGroupMember(ctx, channel.PlatformFeishu, chatID, b.Name()); err != nil {
				logger().Warn("sync groups: ensure member failed", "chat_id", chatID, "error", err)
			} else {
				synced++
			}
			cancel()
		}
		if resp.Data.HasMore == nil || !*resp.Data.HasMore {
			break
		}
		if resp.Data.PageToken != nil {
			pageToken = *resp.Data.PageToken
		} else {
			break
		}
	}
	logger().Info("sync groups completed", "synced", synced)
}
