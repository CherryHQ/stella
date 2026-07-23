package controlplane

import (
	"context"
	"errors"
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/webhook"
)

func newService() *Service {
	// The authorization matrix exercises only the admin gate at Begin, so the
	// persistence/runtime handles are intentionally nil: a denied use case returns
	// at Begin before touching them.
	return NewService(nil, nil, nil, nil, nil)
}

func adminAuthority(t *testing.T) authz.Authority {
	t.Helper()
	a, err := authz.NewUserAuthority(authz.UserID("admin-1"), true)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func userAuthority(t *testing.T) authz.Authority {
	t.Helper()
	a, err := authz.NewUserAuthority(authz.UserID("user-1"), false)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// TestAdminMintsAccess proves an admin authority opens a control-plane Access —
// the sole gate for every provider/settings/plugin/channel operation.
func TestAdminMintsAccess(t *testing.T) {
	acc, err := newService().Begin(context.Background(), adminAuthority(t))
	if err != nil || acc == nil {
		t.Fatalf("admin Begin = (%v, %v), want an Access", acc, err)
	}
}

// TestNonAdminDenied proves a non-admin UserActor is default-denied at Begin —
// the exact contract the legacy requireAdmin gate enforced. No Access is minted,
// so no durable read or external action can run.
func TestNonAdminDenied(t *testing.T) {
	acc, err := newService().Begin(context.Background(), userAuthority(t))
	if !errors.Is(err, authz.ErrForbidden) || acc != nil {
		t.Fatalf("non-admin Begin = (%v, %v), want forbidden and no Access", acc, err)
	}
}

func TestChannelCreateConflictIsStable(t *testing.T) {
	conflict := &ConflictError{}
	if !errors.As(channelCreateError(config.ErrChannelExists), &conflict) || conflict.Msg != "channel already exists" {
		t.Fatalf("channel create conflict = %#v", conflict)
	}
}

func TestEndpointErrorDistinguishesConfigurationRetryFromEndpointRevocation(t *testing.T) {
	configChanged := &ConflictError{}
	ok := errors.As(endpointError(webhook.ErrChannelConfigChanged), &configChanged)
	if !ok {
		t.Fatalf("config-changed error = %T, want *ConflictError", endpointError(webhook.ErrChannelConfigChanged))
	}
	if configChanged.Msg != "channel configuration changed; retry endpoint issuance" {
		t.Fatalf("config-changed message = %q", configChanged.Msg)
	}

	active := &ConflictError{}
	ok = errors.As(endpointError(webhook.ErrChannelEndpointActive), &active)
	if !ok {
		t.Fatalf("endpoint-active error = %T, want *ConflictError", endpointError(webhook.ErrChannelEndpointActive))
	}
	if active.Msg != "webhook endpoint is active; revoke it before changing the channel binding" {
		t.Fatalf("endpoint-active message = %q", active.Msg)
	}
}

// TestBeginFailsClosed locks the fail-closed properties: a nil service denies with
// ErrUnavailable, and an invalid Authority is forbidden before any work.
func TestBeginFailsClosed(t *testing.T) {
	ctx := context.Background()

	var nilSvc *Service
	if _, err := nilSvc.Begin(ctx, adminAuthority(t)); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("nil service Begin err=%v, want ErrUnavailable", err)
	}

	if _, err := newService().Begin(ctx, authz.Authority{}); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("invalid authority Begin err=%v, want forbidden", err)
	}
}
