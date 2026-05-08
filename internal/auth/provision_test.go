package auth_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/auth"
	appdb "github.com/CherryHQ/stella/internal/db"
)

func setupProvisionStore(t *testing.T) auth.AuthStore {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "provision.db")
	db, err := appdb.OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return appdb.NewAuthStore(db)
}

func TestProvisionIdentityUserNew(t *testing.T) {
	store := setupProvisionStore(t)
	ctx := context.Background()

	req := auth.ProvisionRequest{
		Platform:   "feishu",
		ExternalID: "on_abc123",
		Name:       "Alice",
		EmailHint:  "alice@example.com",
	}

	user, err := auth.ProvisionIdentityUser(ctx, store, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if user.ID == 0 {
		t.Fatal("expected non-zero user ID")
	}
	if user.Username != "alice" {
		t.Errorf("username = %q, want %q", user.Username, "alice")
	}
	if user.PasswordHash != "" {
		t.Errorf("password hash should be empty for provisioned users")
	}
	if user.Role != auth.RoleUser {
		t.Errorf("role = %q, want %q", user.Role, auth.RoleUser)
	}
}

func TestProvisionIdentityUserIdempotent(t *testing.T) {
	store := setupProvisionStore(t)
	ctx := context.Background()

	req := auth.ProvisionRequest{
		Platform:   "feishu",
		ExternalID: "on_dup",
		Name:       "Bob",
		EmailHint:  "bob@example.com",
	}

	u1, err := auth.ProvisionIdentityUser(ctx, store, req)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	u2, err := auth.ProvisionIdentityUser(ctx, store, req)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if u1.ID != u2.ID {
		t.Errorf("idempotency: got different user IDs %d vs %d", u1.ID, u2.ID)
	}

	users, _ := store.ListUsers(ctx)
	count := 0
	for _, u := range users {
		if strings.HasPrefix(u.Username, "bob") {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected 1 user with username prefix 'bob', got %d", count)
	}
}

func TestProvisionIdentityUserUsernameCollision(t *testing.T) {
	store := setupProvisionStore(t)
	ctx := context.Background()

	// Two different external IDs that share the same email local-part.
	req1 := auth.ProvisionRequest{
		Platform:   "feishu",
		ExternalID: "on_carol_1",
		Name:       "Carol 1",
		EmailHint:  "carol@example.com",
	}
	req2 := auth.ProvisionRequest{
		Platform:   "feishu",
		ExternalID: "on_carol_2",
		Name:       "Carol 2",
		EmailHint:  "carol@example.com",
	}

	u1, err := auth.ProvisionIdentityUser(ctx, store, req1)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	u2, err := auth.ProvisionIdentityUser(ctx, store, req2)
	if err != nil {
		t.Fatalf("second: %v", err)
	}

	if u1.Username != "carol" {
		t.Errorf("u1.Username = %q, want %q", u1.Username, "carol")
	}
	if u2.Username != "carol-2" {
		t.Errorf("u2.Username = %q, want %q", u2.Username, "carol-2")
	}
	if u1.ID == u2.ID {
		t.Error("collision: same user ID for different external IDs")
	}
}

func TestProvisionIdentityUserEmptyEmail(t *testing.T) {
	store := setupProvisionStore(t)
	ctx := context.Background()

	req := auth.ProvisionRequest{
		Platform:   "feishu",
		ExternalID: "on_xyz12345",
		Name:       "Dave",
		EmailHint:  "",
	}

	user, err := auth.ProvisionIdentityUser(ctx, store, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should fall back to feishu-<first 8 chars of externalID>.
	if user.Username != "feishu-on_xyz12" {
		t.Errorf("username = %q, want %q", user.Username, "feishu-on_xyz12")
	}
}

func TestProvisionIdentityUserShortExternalID(t *testing.T) {
	store := setupProvisionStore(t)
	ctx := context.Background()

	req := auth.ProvisionRequest{
		Platform:   "feishu",
		ExternalID: "short",
		Name:       "Eve",
		EmailHint:  "",
	}

	user, err := auth.ProvisionIdentityUser(ctx, store, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if user.Username != "feishu-short" {
		t.Errorf("username = %q, want %q", user.Username, "feishu-short")
	}
}

func TestProvisionIdentityUserCallsOnUserCreated(t *testing.T) {
	store := setupProvisionStore(t)
	ctx := context.Background()

	var calledUserID int64
	req := auth.ProvisionRequest{
		Platform:   "feishu",
		ExternalID: "on_callback",
		Name:       "Frank",
		EmailHint:  "frank@example.com",
		OnUserCreated: func(_ context.Context, userID int64) error {
			calledUserID = userID
			return nil
		},
	}

	user, err := auth.ProvisionIdentityUser(ctx, store, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calledUserID != user.ID {
		t.Fatalf("OnUserCreated called with user_id=%d, want %d", calledUserID, user.ID)
	}
}

func TestProvisionIdentityUserOnUserCreatedFailureRollsBackUser(t *testing.T) {
	store := setupProvisionStore(t)
	ctx := context.Background()

	req := auth.ProvisionRequest{
		Platform:   "feishu",
		ExternalID: "on_callback_fail",
		Name:       "Grace",
		EmailHint:  "grace@example.com",
		OnUserCreated: func(_ context.Context, _ int64) error {
			return errors.New("boom")
		},
	}

	_, err := auth.ProvisionIdentityUser(ctx, store, req)
	if err == nil {
		t.Fatal("expected error from OnUserCreated")
	}
	if !strings.Contains(err.Error(), "on user created: boom") {
		t.Fatalf("error = %v, want on user created failure", err)
	}

	if _, err := store.GetIdentityByPlatform(ctx, req.Platform, req.ExternalID); err == nil {
		t.Fatal("expected no identity after rollback")
	}
	if _, err := store.GetUserByUsername(ctx, "grace"); err == nil {
		t.Fatal("expected created user to be rolled back")
	}
}

type provisionStubStore struct {
	getIdentityByPlatform func(context.Context, string, string) (auth.Identity, error)
	getUser               func(context.Context, int64) (auth.AuthUser, error)
	getUserByUsername     func(context.Context, string) (auth.AuthUser, error)
	createUser            func(context.Context, string, string) (auth.AuthUser, error)
	deleteUser            func(context.Context, int64) error
	createIdentity        func(context.Context, auth.Identity) (auth.Identity, error)
}

func (s provisionStubStore) CreateUser(ctx context.Context, username, passwordHash string) (auth.AuthUser, error) {
	return s.createUser(ctx, username, passwordHash)
}

func (s provisionStubStore) GetUser(ctx context.Context, id int64) (auth.AuthUser, error) {
	return s.getUser(ctx, id)
}

func (s provisionStubStore) GetUserByUsername(ctx context.Context, username string) (auth.AuthUser, error) {
	return s.getUserByUsername(ctx, username)
}
func (s provisionStubStore) ListUsers(context.Context) ([]auth.AuthUser, error) { panic("unused") }
func (s provisionStubStore) UpdateUser(context.Context, auth.AuthUser) error    { panic("unused") }
func (s provisionStubStore) UpdateUserRole(context.Context, int64, string) error {
	panic("unused")
}

func (s provisionStubStore) UpdateUserDefaultAgent(context.Context, int64, string) error {
	panic("unused")
}

func (s provisionStubStore) UpdateUserNotifyIdentity(context.Context, int64, *int64) error {
	panic("unused")
}

func (s provisionStubStore) UpdateUserAgeKeys(context.Context, int64, string, string) error {
	panic("unused")
}

func (s provisionStubStore) DeleteUser(ctx context.Context, id int64) error {
	return s.deleteUser(ctx, id)
}
func (s provisionStubStore) CountUsers(context.Context) (int64, error) { panic("unused") }
func (s provisionStubStore) CreateIdentity(ctx context.Context, i auth.Identity) (auth.Identity, error) {
	return s.createIdentity(ctx, i)
}

func (s provisionStubStore) GetIdentity(context.Context, int64) (auth.Identity, error) {
	panic("unused")
}

func (s provisionStubStore) GetIdentityByPlatform(ctx context.Context, platform, externalID string) (auth.Identity, error) {
	return s.getIdentityByPlatform(ctx, platform, externalID)
}

func (s provisionStubStore) UpdateIdentityExternalID(context.Context, int64, string) error {
	panic("unused")
}

func (s provisionStubStore) ListIdentitiesByUser(context.Context, int64) ([]auth.Identity, error) {
	panic("unused")
}
func (s provisionStubStore) DeleteIdentity(context.Context, int64) error { panic("unused") }
func (s provisionStubStore) CreatePolicy(context.Context, auth.Policy) (auth.Policy, error) {
	panic("unused")
}
func (s provisionStubStore) GetPolicy(context.Context, string) (auth.Policy, error) { panic("unused") }
func (s provisionStubStore) ListPolicies(context.Context) ([]auth.Policy, error)    { panic("unused") }
func (s provisionStubStore) ListEnabledPolicies(context.Context) ([]auth.Policy, error) {
	panic("unused")
}
func (s provisionStubStore) UpdatePolicy(context.Context, auth.Policy) error  { panic("unused") }
func (s provisionStubStore) DeletePolicy(context.Context, string) error       { panic("unused") }
func (s provisionStubStore) AssignAgent(context.Context, int64, string) error { panic("unused") }
func (s provisionStubStore) RemoveAgent(context.Context, int64, string) error { panic("unused") }
func (s provisionStubStore) ListUserAgentIDs(context.Context, int64) ([]string, error) {
	panic("unused")
}

func (s provisionStubStore) ListAgentUserIDs(context.Context, string) ([]int64, error) {
	panic("unused")
}

func (s provisionStubStore) CreateSession(context.Context, auth.Session) (auth.Session, error) {
	panic("unused")
}

func (s provisionStubStore) GetSession(context.Context, string) (auth.Session, error) {
	panic("unused")
}
func (s provisionStubStore) DeleteSession(context.Context, string) error     { panic("unused") }
func (s provisionStubStore) DeleteExpiredSessions(context.Context) error     { panic("unused") }
func (s provisionStubStore) DeleteUserSessions(context.Context, int64) error { panic("unused") }
func (s provisionStubStore) UpdateSessionExpiry(context.Context, string, time.Time) error {
	panic("unused")
}

func (s provisionStubStore) CreateUserToken(context.Context, auth.UserToken) (auth.UserToken, error) {
	panic("unused")
}

func (s provisionStubStore) GetUserTokenByHash(context.Context, string) (auth.UserToken, error) {
	panic("unused")
}

func (s provisionStubStore) GetActiveUserTokenByHash(context.Context, string) (auth.UserToken, error) {
	panic("unused")
}

func (s provisionStubStore) GetActiveAutoUserToken(context.Context, int64) (auth.UserToken, error) {
	panic("unused")
}

func (s provisionStubStore) RotateUserToken(context.Context, int64) (int64, error) {
	panic("unused")
}

func (s provisionStubStore) RevokeUserToken(context.Context, int64) (int64, error) {
	panic("unused")
}

func (s provisionStubStore) UpdateUserTokenLastUsed(context.Context, int64) (int64, error) {
	panic("unused")
}

func TestProvisionIdentityUserPropagatesIdentityLookupError(t *testing.T) {
	store := provisionStubStore{
		getIdentityByPlatform: func(context.Context, string, string) (auth.Identity, error) {
			return auth.Identity{}, errors.New("db exploded")
		},
	}
	_, err := auth.ProvisionIdentityUser(context.Background(), store, auth.ProvisionRequest{
		Platform:   "feishu",
		ExternalID: "broken",
	})
	if err == nil || !strings.Contains(err.Error(), "check identity: db exploded") {
		t.Fatalf("err = %v, want identity lookup error", err)
	}
}

func TestProvisionIdentityUserReturnsExistingUserFromIdentity(t *testing.T) {
	store := provisionStubStore{
		getIdentityByPlatform: func(context.Context, string, string) (auth.Identity, error) {
			return auth.Identity{UserID: 42}, nil
		},
		getUser: func(context.Context, int64) (auth.AuthUser, error) {
			return auth.AuthUser{ID: 42, Username: "existing"}, nil
		},
	}
	user, err := auth.ProvisionIdentityUser(context.Background(), store, auth.ProvisionRequest{
		Platform:   "feishu",
		ExternalID: "existing",
	})
	if err != nil {
		t.Fatalf("ProvisionIdentityUser: %v", err)
	}
	if user.ID != 42 || user.Username != "existing" {
		t.Fatalf("user = %+v, want existing user", user)
	}
}

func TestProvisionIdentityUserOnUserCreatedCleanupFailureIncludesBothErrors(t *testing.T) {
	store := provisionStubStore{
		getIdentityByPlatform: func(context.Context, string, string) (auth.Identity, error) {
			return auth.Identity{}, sql.ErrNoRows
		},
		getUserByUsername: func(context.Context, string) (auth.AuthUser, error) {
			return auth.AuthUser{}, sql.ErrNoRows
		},
		createUser: func(context.Context, string, string) (auth.AuthUser, error) {
			return auth.AuthUser{ID: 7, Username: "cleanup"}, nil
		},
		deleteUser: func(context.Context, int64) error {
			return errors.New("cleanup failed")
		},
		createIdentity: func(context.Context, auth.Identity) (auth.Identity, error) {
			panic("should not reach createIdentity")
		},
	}
	_, err := auth.ProvisionIdentityUser(context.Background(), store, auth.ProvisionRequest{
		Platform:   "feishu",
		ExternalID: "cleanup",
		EmailHint:  "cleanup@example.com",
		OnUserCreated: func(context.Context, int64) error {
			return errors.New("callback failed")
		},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "callback failed") || !strings.Contains(err.Error(), "cleanup failed") {
		t.Fatalf("err = %v, want callback and cleanup errors", err)
	}
}

func TestProvisionIdentityUserCreateUserError(t *testing.T) {
	store := provisionStubStore{
		getIdentityByPlatform: func(context.Context, string, string) (auth.Identity, error) {
			return auth.Identity{}, sql.ErrNoRows
		},
		getUserByUsername: func(context.Context, string) (auth.AuthUser, error) {
			return auth.AuthUser{}, sql.ErrNoRows
		},
		createUser: func(context.Context, string, string) (auth.AuthUser, error) {
			return auth.AuthUser{}, errors.New("create failed")
		},
	}
	_, err := auth.ProvisionIdentityUser(context.Background(), store, auth.ProvisionRequest{
		Platform:   "feishu",
		ExternalID: "create-fail",
		EmailHint:  "create@example.com",
	})
	if err == nil || !strings.Contains(err.Error(), "create user: create failed") {
		t.Fatalf("err = %v, want create user error", err)
	}
}

func TestProvisionIdentityUserIdentityRaceReturnsWinner(t *testing.T) {
	lookupCalls := 0
	store := provisionStubStore{
		getIdentityByPlatform: func(context.Context, string, string) (auth.Identity, error) {
			lookupCalls++
			if lookupCalls == 1 {
				return auth.Identity{}, sql.ErrNoRows
			}
			return auth.Identity{UserID: 99}, nil
		},
		getUserByUsername: func(context.Context, string) (auth.AuthUser, error) {
			return auth.AuthUser{}, sql.ErrNoRows
		},
		createUser: func(context.Context, string, string) (auth.AuthUser, error) {
			return auth.AuthUser{ID: 7, Username: "racy"}, nil
		},
		deleteUser: func(context.Context, int64) error {
			return nil
		},
		createIdentity: func(context.Context, auth.Identity) (auth.Identity, error) {
			return auth.Identity{}, errors.New("unique constraint")
		},
		getUser: func(context.Context, int64) (auth.AuthUser, error) {
			return auth.AuthUser{ID: 99, Username: "winner"}, nil
		},
	}
	user, err := auth.ProvisionIdentityUser(context.Background(), store, auth.ProvisionRequest{
		Platform:   "feishu",
		ExternalID: "racy",
		EmailHint:  "racy@example.com",
	})
	if err != nil {
		t.Fatalf("ProvisionIdentityUser: %v", err)
	}
	if user.ID != 99 || user.Username != "winner" {
		t.Fatalf("user = %+v, want winner", user)
	}
}
