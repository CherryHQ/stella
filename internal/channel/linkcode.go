package channel

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/vaayne/anna/internal/auth"
)

func TryLinkCode(ctx context.Context, authStore auth.AuthStore, linkCodes *auth.LinkCodeStore, text, platform, senderID, senderName string) (string, bool) {
	code := parseLinkCommand(text)
	if code == "" {
		return "", false
	}

	userID, codePlatform, ok := linkCodes.Consume(code)
	if !ok {
		return "Link code is invalid or has expired (codes last 5 minutes). Generate a new one from the admin profile page and try again.", true
	}

	if codePlatform != platform {
		return fmt.Sprintf("This link code was generated for %s, not %s. Generate a new code and select %s as the platform.", codePlatform, platform, platform), true
	}

	if existing, err := authStore.GetIdentityByPlatform(ctx, platform, senderID); err == nil {
		return fmt.Sprintf("This %s account is already linked to user #%d. Ask an admin to unlink it first from the user management page.", platform, existing.UserID), true
	}

	_, err := authStore.CreateIdentity(ctx, auth.Identity{
		UserID:     userID,
		Platform:   platform,
		ExternalID: senderID,
		Name:       senderName,
	})
	if err != nil {
		slog.Error("link code: create identity failed", "platform", platform, "sender", senderID, "user_id", userID, "error", err)
		return "Failed to link account. Please try again.", true
	}

	slog.Info("link code: account linked", "platform", platform, "sender", senderID, "user_id", userID)
	return "Account linked successfully! Your channel account is now connected to your system user.", true
}

func parseLinkCommand(text string) string {
	text = strings.TrimSpace(text)

	lower := strings.ToLower(text)
	if strings.HasPrefix(lower, "/link ") {
		code := strings.TrimSpace(text[6:])
		if auth.IsLinkCode(code) {
			return code
		}
		return ""
	}

	if auth.IsLinkCode(text) {
		return text
	}

	return ""
}
