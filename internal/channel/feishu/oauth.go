package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/vaayne/anna/internal/feishutool"
)

// handleAuthCommand handles the /auth command.
// If text is "/auth" alone, it sends an OAuth authorization card.
// If text is "/auth <code>", it exchanges the code for tokens.
func (b *Bot) handleAuthCommand(openID, chatID, messageID string, args string) {
	if b.fsClient == nil {
		b.replyText(b.ctx, messageID, "Feishu OAuth is not configured.")
		return
	}

	args = strings.TrimSpace(args)

	if args == "" {
		// Send auth card with authorization URL.
		b.sendAuthCard(openID, chatID, messageID)
		return
	}

	// Treat args as authorization code.
	b.exchangeAuthCode(openID, messageID, args)
}

const defaultRedirectURI = "https://anna.vaayne.com/oauth/callback"

// sendAuthCard sends an interactive card with the OAuth authorization URL.
func (b *Bot) sendAuthCard(openID, chatID, messageID string) {
	redirectURI := b.cfg.RedirectURI
	if redirectURI == "" {
		redirectURI = defaultRedirectURI
	}
	authURL := feishutool.AuthURL(b.cfg.AppID, redirectURI, openID)

	card := map[string]any{
		"schema": "2.0",
		"header": map[string]any{
			"title": map[string]any{
				"tag":     "plain_text",
				"content": "Authorization Required",
			},
			"template": "blue",
		},
		"body": map[string]any{
			"elements": []map[string]any{
				{
					"tag":     "markdown",
					"content": fmt.Sprintf("To use Feishu tools that require your identity (calendar, tasks, etc.), you need to authorize this app.\n\n**[Click here to authorize](%s)**\n\nAfter authorization, copy the code from the page and send it back with `/auth <code>`.", authURL),
				},
			},
		},
	}

	cardJSON, err := json.Marshal(card)
	if err != nil {
		logger().Error("marshal auth card failed", "error", err)
		b.replyText(b.ctx, messageID, fmt.Sprintf("Please visit this URL to authorize:\n%s", authURL))
		return
	}

	b.sendCard(b.ctx, messageID, string(cardJSON))
}

// exchangeAuthCode exchanges an authorization code for tokens and stores them.
// It verifies the token's owner matches the requesting user to prevent
// cross-account token binding.
func (b *Bot) exchangeAuthCode(openID, messageID, code string) {
	token, err := b.fsClient.ExchangeCode(b.ctx, code)
	if err != nil {
		logger().Error("exchange auth code failed", "open_id", openID, "error", err)
		b.replyText(b.ctx, messageID, fmt.Sprintf("Authorization failed: %v\n\nPlease try `/auth` again.", err))
		return
	}

	// Verify the token belongs to the requesting user by calling the
	// user info endpoint with the new access token.
	tokenOpenID, err := b.fsClient.GetTokenOwner(b.ctx, token.AccessToken)
	if err != nil {
		logger().Error("verify token owner failed", "open_id", openID, "error", err)
		b.replyText(b.ctx, messageID, "Authorization failed: could not verify token owner. Please try `/auth` again.")
		return
	}
	if tokenOpenID != openID {
		logger().Warn("OAuth token owner mismatch", "expected", openID, "got", tokenOpenID)
		b.replyText(b.ctx, messageID, "Authorization failed: the authorization code belongs to a different user. Please use your own authorization link.")
		return
	}

	ts := b.fsClient.TokenStore()
	if ts == nil {
		b.replyText(b.ctx, messageID, "Token storage is not configured.")
		return
	}

	if err := ts.Set(b.ctx, openID, token); err != nil {
		logger().Error("store token failed", "open_id", openID, "error", err)
		b.replyText(b.ctx, messageID, "Failed to store authorization token. Please try again.")
		return
	}

	b.replyText(b.ctx, messageID, "Authorization successful! You can now use Feishu tools with your identity.")
}

// sendCard sends an interactive card as a reply to a message.
func (b *Bot) sendCard(ctx context.Context, messageID, cardJSON string) {
	resp, err := b.client.Im.Message.Reply(ctx,
		larkim.NewReplyMessageReqBuilder().
			MessageId(messageID).
			Body(larkim.NewReplyMessageReqBodyBuilder().
				MsgType(larkim.MsgTypeInteractive).
				Content(cardJSON).
				Build()).
			Build())
	if err != nil {
		logger().Error("send card failed", "message_id", messageID, "error", err)
		return
	}
	if !resp.Success() {
		logger().Error("send card failed", "message_id", messageID, "code", resp.Code, "msg", resp.Msg)
	}
}
