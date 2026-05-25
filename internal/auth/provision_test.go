package auth_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/auth"
	appdb "github.com/CherryHQ/stella/internal/db"
)

func setupProvisionStore(t *testing.T) *appdb.OIDCStore {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "provision.db")
	db, err := appdb.OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return appdb.NewOIDCStore(db)
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

	user, err := auth.ProvisionIdentityUser(ctx, store, store, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if user.ID == "" {
		t.Fatal("expected non-zero user ID")
	}
	if user.Email != "alice@example.com" {
		t.Errorf("email = %q, want %q", user.Email, "alice@example.com")
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

	u1, err := auth.ProvisionIdentityUser(ctx, store, store, req)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	u2, err := auth.ProvisionIdentityUser(ctx, store, store, req)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if u1.ID != u2.ID {
		t.Errorf("idempotency: got different user IDs %q vs %q", u1.ID, u2.ID)
	}

	users, _ := store.ListUsers(ctx)
	count := 0
	for _, u := range users {
		if strings.HasPrefix(u.Email, "bob@") {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected 1 user with email prefix 'bob@', got %d", count)
	}
}

func TestProvisionIdentityUserEmailCollision(t *testing.T) {
	store := setupProvisionStore(t)
	ctx := context.Background()

	// Two different external IDs that share the same email hint.
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

	u1, err := auth.ProvisionIdentityUser(ctx, store, store, req1)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	u2, err := auth.ProvisionIdentityUser(ctx, store, store, req2)
	if err != nil {
		t.Fatalf("second: %v", err)
	}

	if u1.Email != "carol@example.com" {
		t.Errorf("u1.Email = %q, want %q", u1.Email, "carol@example.com")
	}
	if u2.Email != "carol-2@example.com" {
		t.Errorf("u2.Email = %q, want %q", u2.Email, "carol-2@example.com")
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

	user, err := auth.ProvisionIdentityUser(ctx, store, store, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should fall back to feishu-<first 8 chars of externalID>@feishu.channel.
	if user.Email != "feishu-on_xyz123@feishu.channel" {
		t.Errorf("email = %q, want %q", user.Email, "feishu-on_xyz123@feishu.channel")
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

	user, err := auth.ProvisionIdentityUser(ctx, store, store, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if user.Email != "feishu-short@feishu.channel" {
		t.Errorf("email = %q, want %q", user.Email, "feishu-short@feishu.channel")
	}
}

func TestProvisionIdentityUserCallsOnUserCreated(t *testing.T) {
	store := setupProvisionStore(t)
	ctx := context.Background()

	var calledUserID string
	req := auth.ProvisionRequest{
		Platform:   "feishu",
		ExternalID: "on_callback",
		Name:       "Frank",
		EmailHint:  "frank@example.com",
		OnUserCreated: func(_ context.Context, userID string) error {
			calledUserID = userID
			return nil
		},
	}

	user, err := auth.ProvisionIdentityUser(ctx, store, store, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calledUserID != user.ID {
		t.Fatalf("OnUserCreated called with user_id=%q, want %q", calledUserID, user.ID)
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
		OnUserCreated: func(_ context.Context, _ string) error {
			return errors.New("boom")
		},
	}

	_, err := auth.ProvisionIdentityUser(ctx, store, store, req)
	if err == nil {
		t.Fatal("expected error from OnUserCreated")
	}
	if !strings.Contains(err.Error(), "on user created: boom") {
		t.Fatalf("error = %v, want on user created failure", err)
	}

	if _, err := store.GetChannelIdentityByPlatform(ctx, req.Platform, req.ExternalID); err == nil {
		t.Fatal("expected no identity after rollback")
	}
	if _, err := store.GetUserByEmail(ctx, "grace@example.com"); err == nil {
		t.Fatal("expected created user to be rolled back")
	}
}

// provisionStubUsers implements auth.UserStore for unit testing.
type provisionStubUsers struct {
	createUser   func(context.Context, auth.User) (auth.User, error)
	getUser      func(context.Context, string) (auth.User, error)
	getUserEmail func(context.Context, string) (auth.User, error)
	deleteUser   func(context.Context, string) error
}

func (s provisionStubUsers) CreateUser(ctx context.Context, u auth.User) (auth.User, error) {
	return s.createUser(ctx, u)
}

func (s provisionStubUsers) GetUser(ctx context.Context, id string) (auth.User, error) {
	return s.getUser(ctx, id)
}

func (s provisionStubUsers) GetUserByEmail(ctx context.Context, email string) (auth.User, error) {
	return s.getUserEmail(ctx, email)
}
func (s provisionStubUsers) ListUsers(context.Context) ([]auth.User, error) { panic("unused") }
func (s provisionStubUsers) UpdateUser(context.Context, auth.User) error    { panic("unused") }
func (s provisionStubUsers) DeleteUser(ctx context.Context, id string) error {
	return s.deleteUser(ctx, id)
}
func (s provisionStubUsers) CountUsers(context.Context) (int64, error) { panic("unused") }
func (s provisionStubUsers) UpdateUserAgeKeys(context.Context, string, string, string) error {
	panic("unused")
}

func (s provisionStubUsers) UpdateUserDefaultAgent(context.Context, string, string) error {
	panic("unused")
}

func (s provisionStubUsers) UpdateUserNotifyIdentity(context.Context, string, *string) error {
	panic("unused")
}

// provisionStubIdents implements auth.ChannelIdentityStore for unit testing.
type provisionStubIdents struct {
	getByPlatform func(context.Context, string, string) (auth.ChannelIdentity, error)
	create        func(context.Context, auth.ChannelIdentity) (auth.ChannelIdentity, error)
}

func (s provisionStubIdents) CreateChannelIdentity(ctx context.Context, i auth.ChannelIdentity) (auth.ChannelIdentity, error) {
	return s.create(ctx, i)
}

func (s provisionStubIdents) GetChannelIdentity(context.Context, string) (auth.ChannelIdentity, error) {
	panic("unused")
}

func (s provisionStubIdents) GetChannelIdentityByPlatform(ctx context.Context, platform, externalID string) (auth.ChannelIdentity, error) {
	return s.getByPlatform(ctx, platform, externalID)
}

func (s provisionStubIdents) ListChannelIdentitiesByUser(context.Context, string) ([]auth.ChannelIdentity, error) {
	panic("unused")
}

func (s provisionStubIdents) UpdateChannelIdentityExternalID(context.Context, string, string) error {
	panic("unused")
}
func (s provisionStubIdents) DeleteChannelIdentity(context.Context, string) error { panic("unused") }

func TestProvisionIdentityUserPropagatesIdentityLookupError(t *testing.T) {
	users := provisionStubUsers{}
	idents := provisionStubIdents{
		getByPlatform: func(context.Context, string, string) (auth.ChannelIdentity, error) {
			return auth.ChannelIdentity{}, errors.New("db exploded")
		},
	}
	_, err := auth.ProvisionIdentityUser(context.Background(), users, idents, auth.ProvisionRequest{
		Platform:   "feishu",
		ExternalID: "broken",
	})
	if err == nil || !strings.Contains(err.Error(), "check identity: db exploded") {
		t.Fatalf("err = %v, want identity lookup error", err)
	}
}

func TestProvisionIdentityUserReturnsExistingUserFromIdentity(t *testing.T) {
	users := provisionStubUsers{
		getUser: func(context.Context, string) (auth.User, error) {
			return auth.User{ID: "42", Email: "existing@example.com"}, nil
		},
	}
	idents := provisionStubIdents{
		getByPlatform: func(context.Context, string, string) (auth.ChannelIdentity, error) {
			return auth.ChannelIdentity{UserID: "42"}, nil
		},
	}
	user, err := auth.ProvisionIdentityUser(context.Background(), users, idents, auth.ProvisionRequest{
		Platform:   "feishu",
		ExternalID: "existing",
	})
	if err != nil {
		t.Fatalf("ProvisionIdentityUser: %v", err)
	}
	if user.ID != "42" || user.Email != "existing@example.com" {
		t.Fatalf("user = %+v, want existing user", user)
	}
}

func TestProvisionIdentityUserOnUserCreatedCleanupFailureIncludesBothErrors(t *testing.T) {
	users := provisionStubUsers{
		getUserEmail: func(context.Context, string) (auth.User, error) {
			return auth.User{}, sql.ErrNoRows
		},
		createUser: func(context.Context, auth.User) (auth.User, error) {
			return auth.User{ID: "7", Email: "cleanup@example.com"}, nil
		},
		deleteUser: func(context.Context, string) error {
			return errors.New("cleanup failed")
		},
	}
	idents := provisionStubIdents{
		getByPlatform: func(context.Context, string, string) (auth.ChannelIdentity, error) {
			return auth.ChannelIdentity{}, sql.ErrNoRows
		},
		create: func(context.Context, auth.ChannelIdentity) (auth.ChannelIdentity, error) {
			panic("should not reach createIdentity")
		},
	}
	_, err := auth.ProvisionIdentityUser(context.Background(), users, idents, auth.ProvisionRequest{
		Platform:   "feishu",
		ExternalID: "cleanup",
		EmailHint:  "cleanup@example.com",
		OnUserCreated: func(context.Context, string) error {
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
	users := provisionStubUsers{
		getUserEmail: func(context.Context, string) (auth.User, error) {
			return auth.User{}, sql.ErrNoRows
		},
		createUser: func(context.Context, auth.User) (auth.User, error) {
			return auth.User{}, errors.New("create failed")
		},
	}
	idents := provisionStubIdents{
		getByPlatform: func(context.Context, string, string) (auth.ChannelIdentity, error) {
			return auth.ChannelIdentity{}, sql.ErrNoRows
		},
	}
	_, err := auth.ProvisionIdentityUser(context.Background(), users, idents, auth.ProvisionRequest{
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
	users := provisionStubUsers{
		getUserEmail: func(context.Context, string) (auth.User, error) {
			return auth.User{}, sql.ErrNoRows
		},
		createUser: func(context.Context, auth.User) (auth.User, error) {
			return auth.User{ID: "7", Email: "racy@example.com"}, nil
		},
		deleteUser: func(context.Context, string) error {
			return nil
		},
		getUser: func(context.Context, string) (auth.User, error) {
			return auth.User{ID: "99", Email: "winner@example.com"}, nil
		},
	}
	idents := provisionStubIdents{
		getByPlatform: func(ctx context.Context, platform, externalID string) (auth.ChannelIdentity, error) {
			lookupCalls++
			if lookupCalls == 1 {
				return auth.ChannelIdentity{}, sql.ErrNoRows
			}
			return auth.ChannelIdentity{UserID: "99"}, nil
		},
		create: func(context.Context, auth.ChannelIdentity) (auth.ChannelIdentity, error) {
			return auth.ChannelIdentity{}, errors.New("unique constraint")
		},
	}
	user, err := auth.ProvisionIdentityUser(context.Background(), users, idents, auth.ProvisionRequest{
		Platform:   "feishu",
		ExternalID: "racy",
		EmailHint:  "racy@example.com",
	})
	if err != nil {
		t.Fatalf("ProvisionIdentityUser: %v", err)
	}
	if user.ID != "99" || user.Email != "winner@example.com" {
		t.Fatalf("user = %+v, want winner", user)
	}
}
