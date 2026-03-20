package channel

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/vaayne/anna/internal/auth"
)

// TryLinkCode checks if a message text is a valid link code. If so, it
// consumes the code, creates an auth_identity linking the sender's channel
// account to the system user, and returns a response message + true.
// Returns ("", false) if the text is not a link code.
func TryLinkCode(ctx context.Context, authStore auth.AuthStore, linkCodes *auth.LinkCodeStore, text, platform, senderID, senderName string) (string, bool) {
	text = strings.TrimSpace(text)
	if !auth.IsLinkCode(text) {
		return "", false
	}

	userID, codePlatform, ok := linkCodes.Consume(text)
	if !ok {
		return "Invalid or expired link code. Please generate a new one from the admin profile page.", true
	}

	// Verify the code was generated for this platform.
	if codePlatform != platform {
		return fmt.Sprintf("This link code was generated for %s, not %s. Please generate a new code for the correct platform.", codePlatform, platform), true
	}

	// Check if this identity is already linked.
	if existing, err := authStore.GetIdentityByPlatform(ctx, platform, senderID); err == nil {
		return fmt.Sprintf("This %s account is already linked to user ID %d. Please unlink it first from the admin profile page.", platform, existing.UserID), true
	}

	// Create the identity link.
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
