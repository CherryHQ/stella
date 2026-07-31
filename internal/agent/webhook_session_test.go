package agent_test

import (
	"context"
	"testing"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/authz"
)

func webhookAuthority(t *testing.T, userID string) authz.Authority {
	t.Helper()
	authority, err := (auth.Subject{UserID: userID, Roles: []string{auth.RoleUser}}).Authority()
	if err != nil {
		t.Fatalf("authority: %v", err)
	}
	return authority
}

// TestResolvePrivateChannelSessionKeepsExactIDSemantics is the webhook
// regression for the chat-channel binding change: a persistent webhook still
// addresses exactly one session whose id IS its derived key, and it resolves to
// the same session on every call. Webhooks have no `/new`, and their channel
// (ChannelWebhook) is deliberately not their key, so binding-based resolution
// would either miss them or merge a user's webhooks into one session.
func TestResolvePrivateChannelSessionKeepsExactIDSemantics(t *testing.T) {
	svc, _ := newTestService(t, nil)
	ctx := context.Background()
	authority := webhookAuthority(t, "u1")
	key := agent.BuildUserSessionKey("agent1", "u1", "webhook:hook-1")

	first, err := svc.ResolvePrivateChannelSession(ctx, authority, key, "u1", "agent1", session.ChannelWebhook)
	if err != nil {
		t.Fatalf("ResolvePrivateChannelSession: %v", err)
	}
	if first.ID != key {
		t.Fatalf("session id = %q, want the webhook key %q", first.ID, key)
	}
	if first.Channel != string(session.ChannelWebhook) {
		t.Fatalf("channel = %q, want %q", first.Channel, session.ChannelWebhook)
	}

	second, err := svc.ResolvePrivateChannelSession(ctx, authority, key, "u1", "agent1", session.ChannelWebhook)
	if err != nil {
		t.Fatalf("second ResolvePrivateChannelSession: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("second resolve = %q, want the same webhook session %q", second.ID, first.ID)
	}

	// A second webhook for the same user must stay a separate session.
	otherKey := agent.BuildUserSessionKey("agent1", "u1", "webhook:hook-2")
	other, err := svc.ResolvePrivateChannelSession(ctx, authority, otherKey, "u1", "agent1", session.ChannelWebhook)
	if err != nil {
		t.Fatalf("ResolvePrivateChannelSession for a second webhook: %v", err)
	}
	if other.ID == first.ID {
		t.Fatal("two webhooks of one user must not share a session")
	}
}

// TestWebhookSessionCanUseKnowledgeSearch pins the authorization contract:
// a webhook authorized to represent its trusted user and Agent receives the
// same knowledge tools as other authorized user-representing private runs.
func TestWebhookSessionCanUseKnowledgeSearch(t *testing.T) {
	svc, _ := newTestService(t, nil)
	ctx := context.Background()
	authority := webhookAuthority(t, "u1")
	key := agent.BuildUserSessionKey("agent1", "u1", "webhook:hook-knowledge")

	info, err := svc.ResolvePrivateChannelSession(ctx, authority, key, "u1", "agent1", session.ChannelWebhook)
	if err != nil {
		t.Fatalf("ResolvePrivateChannelSession: %v", err)
	}
	if info.Channel != string(session.ChannelWebhook) {
		t.Fatalf("channel = %q, want %q", info.Channel, session.ChannelWebhook)
	}
	if !agent.KnowledgeToolAvailable(ctx, agent.RunnerParams{
		UserID:      info.UserID,
		GroupID:     info.GroupID,
		AgentID:     info.AgentID,
		SessionKind: info.Kind,
	}) {
		t.Fatal("authorized webhook session must receive knowledge_search")
	}
}

// TestChatChannelBindingDoesNotCaptureWebhookSessions proves the new chat
// binding cannot adopt a webhook session: their channels differ, and a chat
// rotating a webhook's session out from under it would break the webhook.
func TestChatChannelBindingDoesNotCaptureWebhookSessions(t *testing.T) {
	svc, _ := newTestService(t, nil)
	ctx := context.Background()
	authority := webhookAuthority(t, "u1")

	hookKey := agent.BuildUserSessionKey("agent1", "u1", "webhook:hook-1")
	hook, err := svc.ResolvePrivateChannelSession(ctx, authority, hookKey, "u1", "agent1", session.ChannelWebhook)
	if err != nil {
		t.Fatalf("ResolvePrivateChannelSession: %v", err)
	}

	chatKey := agent.BuildSessionKey("agent1", "telegram", "ext-1", "private")
	chat, err := svc.ResolveChatChannelSession(ctx, agent.ChatChannelRequest{
		Authority:  authority,
		UserID:     "u1",
		AgentID:    "agent1",
		Channel:    session.Channel(chatKey),
		SessionKey: chatKey,
	})
	if err != nil {
		t.Fatalf("ResolveChatChannelSession: %v", err)
	}
	if chat.ID == hook.ID {
		t.Fatal("a chat channel must not bind to a webhook session")
	}

	after, err := svc.ResolvePrivateChannelSession(ctx, authority, hookKey, "u1", "agent1", session.ChannelWebhook)
	if err != nil {
		t.Fatalf("ResolvePrivateChannelSession after the chat resolve: %v", err)
	}
	if after.ID != hook.ID || after.Archived {
		t.Fatalf("webhook session = %+v, want it unchanged and active", after)
	}
}
