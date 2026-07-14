package controlplane

import (
	"context"
	"errors"
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
)

func newService() *Service {
	// The authorization matrix exercises only the admin gate at Begin, so the
	// persistence/runtime handles are intentionally nil: a denied use case returns
	// at Begin before touching them.
	return NewService(nil, nil, nil, nil, nil)
}

func adminAuthority(t *testing.T) authz.Authority {
	t.Helper()
	rs, err := authz.NewRoleSet(authz.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	a, err := authz.NewUserAuthority(authz.UserID("admin-1"), rs, authz.GrantSet{})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func userAuthority(t *testing.T) authz.Authority {
	t.Helper()
	rs, err := authz.NewRoleSet(authz.RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	a, err := authz.NewUserAuthority(authz.UserID("user-1"), rs, authz.GrantSet{})
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
