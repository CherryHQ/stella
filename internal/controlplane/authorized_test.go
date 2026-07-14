package controlplane

import (
	"context"
	"errors"
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/authz/policy"
)

// countingAuthorizer proves the control-plane PEP opens exactly one Begin per use
// case, wrapping the real static policy authorizer.
type countingAuthorizer struct {
	authz.Authorizer
	begins int
}

func (a *countingAuthorizer) Begin(ctx context.Context, authority authz.Authority) (authz.Evaluation, error) {
	a.begins++
	return a.Authorizer.Begin(ctx, authority)
}

func newService(t *testing.T) (*Service, *countingAuthorizer) {
	t.Helper()
	az := &countingAuthorizer{Authorizer: policy.New()}
	// The authorization matrix exercises only the decision path (Begin + Decide),
	// so the persistence/runtime handles are intentionally nil: a denied use case
	// returns before touching them, and an allowed decision is asserted directly.
	return NewService(az, nil, nil, nil, nil, nil), az
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

// controlPlaneCases is the full manage+read+list matrix across the four
// admin-only control-plane resources. Each fn decides exactly one action.
func controlPlaneCases() []struct {
	name string
	fn   func(a *Access) error
} {
	return []struct {
		name string
		fn   func(a *Access) error
	}{
		{"provider.manage", func(a *Access) error { return a.authorizeProvider(authz.ActionManage, "p") }},
		{"provider.read", func(a *Access) error { return a.authorizeProvider(authz.ActionRead, "p") }},
		{"provider.list", func(a *Access) error { return a.authorizeProviderList() }},
		{"settings.manage", func(a *Access) error {
			return a.authorizeSettings(authz.ActionManage, "embedding")
		}},
		{"settings.read", func(a *Access) error {
			return a.authorizeSettings(authz.ActionRead, "embedding")
		}},
		{"plugin.manage", func(a *Access) error { return a.authorizePlugin(authz.ActionManage, "tool/x") }},
		{"plugin.read", func(a *Access) error { return a.authorizePlugin(authz.ActionRead, "tool/x") }},
		{"plugin.list", func(a *Access) error { return a.authorizePluginList() }},
		{"channel.manage", func(a *Access) error { return a.authorizeChannel(authz.ActionManage, "c") }},
		{"channel.read", func(a *Access) error { return a.authorizeChannel(authz.ActionRead, "c") }},
		{"channel.list", func(a *Access) error { return a.authorizeChannelList() }},
	}
}

// TestAdminAllowedAllControlPlaneActions proves the admin-full-access built-in is
// the sole grant: a RoleAdmin actor is allowed manage/read/list on every
// control-plane resource, one Begin per use case.
func TestAdminAllowedAllControlPlaneActions(t *testing.T) {
	svc, az := newService(t)
	ctx := context.Background()
	for _, c := range controlPlaneCases() {
		before := az.begins
		acc, err := svc.Begin(ctx, adminAuthority(t))
		if err != nil {
			t.Fatalf("%s: admin Begin: %v", c.name, err)
		}
		if az.begins != before+1 {
			t.Fatalf("%s: Begin count = %d, want exactly 1 per use case", c.name, az.begins-before)
		}
		if err := c.fn(acc); err != nil {
			t.Fatalf("%s: admin decision err=%v, want allowed", c.name, err)
		}
	}
}

// TestNonAdminDeniedAllControlPlaneActions proves a non-admin UserActor is
// default-denied on every control-plane action — the exact contract the legacy
// requireAdmin gate enforced.
func TestNonAdminDeniedAllControlPlaneActions(t *testing.T) {
	svc, _ := newService(t)
	ctx := context.Background()
	for _, c := range controlPlaneCases() {
		acc, err := svc.Begin(ctx, userAuthority(t))
		if err != nil {
			t.Fatalf("%s: user Begin: %v", c.name, err)
		}
		if err := c.fn(acc); !errors.Is(err, authz.ErrForbidden) {
			t.Fatalf("%s: non-admin decision err=%v, want forbidden", c.name, err)
		}
	}
}

// TestBeginFailsClosed locks the two fail-closed properties: a nil authorizer
// denies at Begin, and an invalid Authority is forbidden.
func TestBeginFailsClosed(t *testing.T) {
	ctx := context.Background()

	nilAz := NewService(nil, nil, nil, nil, nil, nil)
	if _, err := nilAz.Begin(ctx, adminAuthority(t)); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("nil authorizer Begin err=%v, want ErrUnavailable", err)
	}

	svc, _ := newService(t)
	if _, err := svc.Begin(ctx, authz.Authority{}); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("invalid authority Begin err=%v, want forbidden", err)
	}
}
