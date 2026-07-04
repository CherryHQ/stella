package notify

import (
	"context"
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

type mockNotifier struct {
	notifyCalls     []pkgchannel.Notification
	notifyUserCalls []struct {
		userID string
		n      pkgchannel.Notification
	}
}

func (m *mockNotifier) Notify(_ context.Context, n pkgchannel.Notification) error {
	m.notifyCalls = append(m.notifyCalls, n)
	return nil
}

func (m *mockNotifier) NotifyUser(_ context.Context, userID string, n pkgchannel.Notification) error {
	m.notifyUserCalls = append(m.notifyUserCalls, struct {
		userID string
		n      pkgchannel.Notification
	}{userID: userID, n: n})
	return nil
}

func TestNotifyToolUsesNotifyUserForScopedUser(t *testing.T) {
	notifier := &mockNotifier{}
	tool := NewTool(notifier)
	ctx := authz.WithUserID(context.Background(), "7")

	_, err := tool.Execute(ctx, map[string]any{"message": "hello"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(notifier.notifyUserCalls) != 1 {
		t.Fatalf("NotifyUser calls = %d, want 1", len(notifier.notifyUserCalls))
	}
	if notifier.notifyUserCalls[0].userID != "7" {
		t.Fatalf("NotifyUser userID = %q, want '7'", notifier.notifyUserCalls[0].userID)
	}
	if len(notifier.notifyCalls) != 0 {
		t.Fatalf("Notify calls = %d, want 0", len(notifier.notifyCalls))
	}
}

func TestNotifyToolUsesNotifyForExplicitTarget(t *testing.T) {
	notifier := &mockNotifier{}
	tool := NewTool(notifier)
	ctx := authz.WithUserID(context.Background(), "7")

	_, err := tool.Execute(ctx, map[string]any{"message": "hello", "channel": "telegram"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(notifier.notifyCalls) != 1 {
		t.Fatalf("Notify calls = %d, want 1", len(notifier.notifyCalls))
	}
	if len(notifier.notifyUserCalls) != 0 {
		t.Fatalf("NotifyUser calls = %d, want 0", len(notifier.notifyUserCalls))
	}
}

func TestNewToolReturnsNilForNilNotifier(t *testing.T) {
	if tool := NewTool(nil); tool != nil {
		t.Fatal("expected nil tool for nil notifier")
	}
}
