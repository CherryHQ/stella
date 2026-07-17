package account

import (
	"context"
	"errors"
	"testing"

	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/authz"
)

// Fakes embed the store interfaces and override only the methods each test
// exercises; an unused method left nil panics if called, which keeps the fakes
// honest about what a use case actually touches.

type fakeUsers struct {
	auth.UserStore
	users      map[string]auth.User
	roleSet    map[string]string
	activeSet  map[string]bool
	defaultSet map[string]string
	updateErr  error
}

func (f *fakeUsers) GetUser(_ context.Context, id string) (auth.User, error) {
	u, ok := f.users[id]
	if !ok {
		return auth.User{}, auth.ErrNotFound
	}
	return u, nil
}

func (f *fakeUsers) ListUsersPaged(_ context.Context, limit, offset int64) ([]auth.User, error) {
	out := make([]auth.User, 0, len(f.users))
	for _, u := range f.users {
		out = append(out, u)
	}
	return out, nil
}

func (f *fakeUsers) UpdateUserRole(_ context.Context, id, role string) error {
	if f.updateErr != nil {
		return f.updateErr
	}
	if f.roleSet == nil {
		f.roleSet = map[string]string{}
	}
	f.roleSet[id] = role
	return nil
}

func (f *fakeUsers) UpdateUserActive(_ context.Context, id string, active bool) error {
	if f.updateErr != nil {
		return f.updateErr
	}
	if f.activeSet == nil {
		f.activeSet = map[string]bool{}
	}
	f.activeSet[id] = active
	return nil
}

func (f *fakeUsers) UpdateUserDefaultAgent(_ context.Context, id, agentID string) error {
	if f.defaultSet == nil {
		f.defaultSet = map[string]string{}
	}
	f.defaultSet[id] = agentID
	return nil
}

type fakeChannels struct {
	auth.ChannelIdentityStore
	byID    map[string]auth.ChannelIdentity
	deleted []string
}

func (f *fakeChannels) ListChannelIdentitiesByUser(_ context.Context, userID string) ([]auth.ChannelIdentity, error) {
	out := []auth.ChannelIdentity{}
	for _, c := range f.byID {
		if c.UserID == userID {
			out = append(out, c)
		}
	}
	return out, nil
}

func (f *fakeChannels) GetChannelIdentity(_ context.Context, id string) (auth.ChannelIdentity, error) {
	c, ok := f.byID[id]
	if !ok {
		return auth.ChannelIdentity{}, auth.ErrNotFound
	}
	return c, nil
}

func (f *fakeChannels) DeleteChannelIdentity(_ context.Context, id string) error {
	f.deleted = append(f.deleted, id)
	delete(f.byID, id)
	return nil
}

type fakeSessions struct {
	auth.SessionStore
	byID          map[string]auth.Session
	deletedUser   []string
	deletedID     []string
	deleteUserErr error
}

func (f *fakeSessions) GetSession(_ context.Context, id string) (auth.Session, error) {
	s, ok := f.byID[id]
	if !ok {
		return auth.Session{}, auth.ErrNotFound
	}
	return s, nil
}

func (f *fakeSessions) DeleteSession(_ context.Context, id string) error {
	f.deletedID = append(f.deletedID, id)
	return nil
}

func (f *fakeSessions) DeleteUserSessions(_ context.Context, userID string) error {
	f.deletedUser = append(f.deletedUser, userID)
	return f.deleteUserErr
}

type fakeCreds struct {
	auth.CredentialStore
}

type fakeAssign struct {
	byUser             map[string][]string
	assigned, removed  []string
	assignErr, listErr error
}

func (f *fakeAssign) ListUserAgentIDs(_ context.Context, userID string) ([]string, error) {
	return f.byUser[userID], f.listErr
}

func (f *fakeAssign) AssignAgent(_ context.Context, userID, agentID string) error {
	f.assigned = append(f.assigned, agentID)
	f.byUser[userID] = append(f.byUser[userID], agentID)
	return f.assignErr
}

func (f *fakeAssign) RemoveAgent(_ context.Context, userID, agentID string) error {
	f.removed = append(f.removed, agentID)
	return nil
}

type fakePATs struct {
	revoked []string
	err     error
}

func (f *fakePATs) RevokeUserPATs(_ context.Context, userID string) (int64, error) {
	f.revoked = append(f.revoked, userID)
	return 0, f.err
}

func userAuthority(t *testing.T, id string, admin bool) authz.Authority {
	t.Helper()
	a, err := authz.NewUserAuthority(authz.UserID(id), admin)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func newService(users *fakeUsers, channels *fakeChannels, sessions *fakeSessions, assign *fakeAssign, pats *fakePATs) *Service {
	return NewService(users, channels, nil, sessions, &fakeCreds{}, assign, pats, nil)
}

func TestGetUserForeignIsOpaque(t *testing.T) {
	ctx := context.Background()
	users := &fakeUsers{users: map[string]auth.User{"u1": {ID: "u1"}, "u2": {ID: "u2"}}}
	svc := newService(users, &fakeChannels{byID: map[string]auth.ChannelIdentity{}}, &fakeSessions{}, nil, nil)

	// A non-admin cannot see another user: opaque not-found, never forbidden.
	if _, err := svc.GetUser(ctx, userAuthority(t, "u1", false), "u2"); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("foreign get = %v, want ErrUserNotFound", err)
	}
	// Self and admin succeed.
	if _, err := svc.GetUser(ctx, userAuthority(t, "u1", false), "u1"); err != nil {
		t.Fatalf("self get = %v", err)
	}
	if _, err := svc.GetUser(ctx, userAuthority(t, "admin", true), "u2"); err != nil {
		t.Fatalf("admin get = %v", err)
	}
}

func TestUpdateRoleRevokesSessions(t *testing.T) {
	ctx := context.Background()
	users := &fakeUsers{users: map[string]auth.User{"u2": {ID: "u2", Role: auth.RoleUser}}}
	sessions := &fakeSessions{}
	svc := newService(users, &fakeChannels{byID: map[string]auth.ChannelIdentity{}}, sessions, nil, nil)

	if _, err := svc.UpdateRole(ctx, userAuthority(t, "admin", true), "u2", auth.RoleAdmin); err != nil {
		t.Fatalf("update role = %v", err)
	}
	if users.roleSet["u2"] != auth.RoleAdmin {
		t.Fatalf("role = %q, want admin", users.roleSet["u2"])
	}
	if len(sessions.deletedUser) != 1 || sessions.deletedUser[0] != "u2" {
		t.Fatalf("sessions revoked = %v, want [u2]", sessions.deletedUser)
	}
}

func TestUpdateRoleSessionRevokeFailurePropagates(t *testing.T) {
	ctx := context.Background()
	users := &fakeUsers{users: map[string]auth.User{"u2": {ID: "u2"}}}
	sessions := &fakeSessions{deleteUserErr: errors.New("revoke down")}
	svc := newService(users, &fakeChannels{byID: map[string]auth.ChannelIdentity{}}, sessions, nil, nil)

	_, err := svc.UpdateRole(ctx, userAuthority(t, "admin", true), "u2", auth.RoleAdmin)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("role revoke failure = %v, want ErrUnavailable", err)
	}
	// The role write still landed; retrying is safe (idempotent).
	if users.roleSet["u2"] != auth.RoleAdmin {
		t.Fatal("role should persist even when the follow-up revocation fails")
	}
}

func TestUpdateRoleGuards(t *testing.T) {
	ctx := context.Background()
	users := &fakeUsers{users: map[string]auth.User{"admin": {ID: "admin", Role: auth.RoleAdmin}}}
	svc := newService(users, &fakeChannels{byID: map[string]auth.ChannelIdentity{}}, &fakeSessions{}, nil, nil)

	if _, err := svc.UpdateRole(ctx, userAuthority(t, "u1", false), "u2", auth.RoleAdmin); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-admin = %v, want ErrForbidden", err)
	}
	if _, err := svc.UpdateRole(ctx, userAuthority(t, "admin", true), "u2", "wizard"); !errors.Is(err, ErrInvalidRole) {
		t.Fatalf("bad role = %v, want ErrInvalidRole", err)
	}
	if _, err := svc.UpdateRole(ctx, userAuthority(t, "admin", true), "admin", auth.RoleUser); !errors.Is(err, ErrSelfRoleRemoval) {
		t.Fatalf("self-demote = %v, want ErrSelfRoleRemoval", err)
	}
}

func TestSetActiveDeactivationLocksDown(t *testing.T) {
	ctx := context.Background()
	users := &fakeUsers{users: map[string]auth.User{"u2": {ID: "u2", IsActive: true}}}
	sessions := &fakeSessions{}
	pats := &fakePATs{}
	svc := newService(users, &fakeChannels{byID: map[string]auth.ChannelIdentity{}}, sessions, nil, pats)

	if _, err := svc.SetActive(ctx, userAuthority(t, "admin", true), "u2", false); err != nil {
		t.Fatalf("deactivate = %v", err)
	}
	if users.activeSet["u2"] {
		t.Fatal("user should be inactive")
	}
	if len(sessions.deletedUser) != 1 {
		t.Fatalf("sessions revoked = %v, want 1", sessions.deletedUser)
	}
	if len(pats.revoked) != 1 {
		t.Fatalf("pats revoked = %v, want 1", pats.revoked)
	}
}

func TestSetActivePartialFailureStillAttemptsBothAndReports(t *testing.T) {
	ctx := context.Background()
	users := &fakeUsers{users: map[string]auth.User{"u2": {ID: "u2", IsActive: true}}}
	sessions := &fakeSessions{deleteUserErr: errors.New("sessions down")}
	pats := &fakePATs{}
	svc := newService(users, &fakeChannels{byID: map[string]auth.ChannelIdentity{}}, sessions, nil, pats)

	_, err := svc.SetActive(ctx, userAuthority(t, "admin", true), "u2", false)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("partial failure = %v, want ErrUnavailable", err)
	}
	// A failed session revoke must not skip the PAT revoke: the account is still
	// driven toward full lockdown, and the error signals a retry is needed.
	if len(pats.revoked) != 1 {
		t.Fatalf("pats revoked = %v, want 1 despite session-revoke failure", pats.revoked)
	}
}

func TestSetActiveGuards(t *testing.T) {
	ctx := context.Background()
	users := &fakeUsers{users: map[string]auth.User{"admin": {ID: "admin"}}}
	sessions := &fakeSessions{}
	pats := &fakePATs{}
	svc := newService(users, &fakeChannels{byID: map[string]auth.ChannelIdentity{}}, sessions, nil, pats)

	if _, err := svc.SetActive(ctx, userAuthority(t, "admin", true), "admin", false); !errors.Is(err, ErrSelfDeactivate) {
		t.Fatalf("self-deactivate = %v, want ErrSelfDeactivate", err)
	}
	// Reactivation performs no revocation.
	if _, err := svc.SetActive(ctx, userAuthority(t, "admin", true), "admin", true); err != nil {
		t.Fatalf("reactivate = %v", err)
	}
	if len(sessions.deletedUser) != 0 || len(pats.revoked) != 0 {
		t.Fatalf("reactivate must not revoke; sessions=%v pats=%v", sessions.deletedUser, pats.revoked)
	}
}

func TestDeleteSessionOwnership(t *testing.T) {
	ctx := context.Background()
	sessions := &fakeSessions{byID: map[string]auth.Session{
		"s1": {ID: "s1", UserID: "u1"},
		"s2": {ID: "s2", UserID: "u2"},
	}}
	svc := newService(&fakeUsers{}, &fakeChannels{}, sessions, nil, nil)

	if err := svc.DeleteSession(ctx, userAuthority(t, "u1", false), "missing"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("missing = %v, want ErrSessionNotFound", err)
	}
	if err := svc.DeleteSession(ctx, userAuthority(t, "u1", false), "s2"); !errors.Is(err, ErrSessionForeign) {
		t.Fatalf("foreign = %v, want ErrSessionForeign", err)
	}
	if err := svc.DeleteSession(ctx, userAuthority(t, "u1", false), "s1"); err != nil {
		t.Fatalf("own = %v", err)
	}
	if len(sessions.deletedID) != 1 || sessions.deletedID[0] != "s1" {
		t.Fatalf("deleted = %v, want [s1]", sessions.deletedID)
	}
}

func TestDeleteUserChannelIdentityOwnership(t *testing.T) {
	ctx := context.Background()
	channels := &fakeChannels{byID: map[string]auth.ChannelIdentity{
		"i1": {ID: "i1", UserID: "u2"},
		"i2": {ID: "i2", UserID: "other"},
	}}
	svc := newService(&fakeUsers{}, channels, &fakeSessions{}, nil, nil)
	admin := userAuthority(t, "admin", true)

	if err := svc.DeleteUserChannelIdentity(ctx, admin, "u2", "missing"); !errors.Is(err, ErrIdentityNotFound) {
		t.Fatalf("missing = %v, want ErrIdentityNotFound", err)
	}
	if err := svc.DeleteUserChannelIdentity(ctx, admin, "u2", "i2"); !errors.Is(err, ErrIdentityNotOwnedByTarget) {
		t.Fatalf("foreign = %v, want ErrIdentityNotOwnedByTarget", err)
	}
	if err := svc.DeleteUserChannelIdentity(ctx, admin, "u2", "i1"); err != nil {
		t.Fatalf("own = %v", err)
	}
	if err := svc.DeleteUserChannelIdentity(ctx, userAuthority(t, "u1", false), "u2", "i1"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-admin = %v, want ErrForbidden", err)
	}
}

func TestUnlinkSelfChannelIdentity(t *testing.T) {
	ctx := context.Background()
	channels := &fakeChannels{byID: map[string]auth.ChannelIdentity{
		"i1": {ID: "i1", UserID: "u1"},
		"i2": {ID: "i2", UserID: "u2"},
	}}
	svc := newService(&fakeUsers{}, channels, &fakeSessions{}, nil, nil)

	if err := svc.UnlinkSelfChannelIdentity(ctx, userAuthority(t, "u1", false), "i2"); !errors.Is(err, ErrIdentityNotOwnedBySelf) {
		t.Fatalf("foreign = %v, want ErrIdentityNotOwnedBySelf", err)
	}
	if err := svc.UnlinkSelfChannelIdentity(ctx, userAuthority(t, "u1", false), "i1"); err != nil {
		t.Fatalf("own = %v", err)
	}
}

func TestSetUserAgentsReconciles(t *testing.T) {
	ctx := context.Background()
	users := &fakeUsers{users: map[string]auth.User{"u2": {ID: "u2"}}}
	assign := &fakeAssign{byUser: map[string][]string{"u2": {"a", "b"}}}
	svc := NewService(users, &fakeChannels{byID: map[string]auth.ChannelIdentity{}}, nil, &fakeSessions{}, &fakeCreds{}, assign, nil, nil)

	if _, err := svc.SetUserAgents(ctx, userAuthority(t, "admin", true), "u2", []string{"b", "c"}); err != nil {
		t.Fatalf("set = %v", err)
	}
	// a removed (not desired), c added (not current), b untouched.
	if len(assign.removed) != 1 || assign.removed[0] != "a" {
		t.Fatalf("removed = %v, want [a]", assign.removed)
	}
	if len(assign.assigned) != 1 || assign.assigned[0] != "c" {
		t.Fatalf("assigned = %v, want [c]", assign.assigned)
	}
}
