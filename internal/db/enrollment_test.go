package db

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/pkg/identity"
)

func setupEnrollment(t *testing.T, recipient *age.X25519Recipient) (*OIDCStore, *auth.AccountEnrollmentService, context.Context) {
	t.Helper()
	pool := newTestDB(t)
	store := NewOIDCStore(pool)
	ctx := context.Background()
	if _, err := store.CreateUser(ctx, auth.User{
		ID: uuid.NewString(), Email: "admin@example.test", Name: "Admin", Role: auth.RoleAdmin,
	}); err != nil {
		t.Fatalf("create admin: %v", err)
	}
	return store, auth.NewAccountEnrollmentService(store, recipient), ctx
}

func enrollmentInput() auth.AccountEnrollmentInput {
	return auth.AccountEnrollmentInput{Namespace: "feishu", Subject: "on_member", Email: "member@example.test", Name: "Member", Claims: map[string]string{"tenant_key": "tenant-1"}}
}

func enrollmentCounts(t *testing.T, store *OIDCStore) (users, logins, channels int) {
	t.Helper()
	ctx := context.Background()
	for _, item := range []struct {
		query string
		out   *int
	}{
		{"SELECT COUNT(*) FROM auth_user", &users},
		{"SELECT COUNT(*) FROM auth_identity", &logins},
		{"SELECT COUNT(*) FROM channel_identity", &channels},
	} {
		if err := store.db.QueryRow(ctx, item.query).Scan(item.out); err != nil {
			t.Fatalf("count rows: %v", err)
		}
	}
	return users, logins, channels
}

func TestFeishuEnrollmentCreatesConvergedRegularUser(t *testing.T) {
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	store, enrollment, ctx := setupEnrollment(t, identity.Recipient())

	result, err := enrollment.Enroll(ctx, enrollmentInput())
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	if !result.Created || result.User.Role != auth.RoleUser || !result.User.IsActive {
		t.Fatalf("result = %+v, want new active regular user", result)
	}
	if result.User.AgePublicKey == "" || result.User.AgePrivateKey == "" {
		t.Fatal("vault-enabled enrollment did not persist age keys")
	}
	if users, logins, channels := enrollmentCounts(t, store); users != 2 || logins != 1 || channels != 1 {
		t.Fatalf("row counts = users:%d logins:%d channels:%d, want 2:1:1", users, logins, channels)
	}

	sessions, err := auth.NewSessionManager(store, "test-vault-key")
	if err != nil {
		t.Fatal(err)
	}
	login, err := auth.NewAuthService(nil, store, store, store).ProcessOIDCLogin(ctx, auth.ExternalIdentity{
		Provider: "feishu", Subject: "on_member", Email: "member@example.test", Name: "Member",
	}, sessions)
	if err != nil {
		t.Fatalf("ProcessOIDCLogin: %v", err)
	}
	if login.User.ID != result.User.ID || login.IsNewUser {
		t.Fatalf("OAuth login = %+v, want provisioned user %s", login, result.User.ID)
	}
}

func TestFeishuEnrollmentWithoutVaultLeavesKeysEmpty(t *testing.T) {
	_, enrollment, ctx := setupEnrollment(t, nil)
	result, err := enrollment.Enroll(ctx, enrollmentInput())
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	if result.User.AgePublicKey != "" || result.User.AgePrivateKey != "" {
		t.Fatalf("vault-disabled keys = %q, %q", result.User.AgePublicKey, result.User.AgePrivateKey)
	}
}

func TestAccountEnrollmentPersistsPluginNormalizedSyntheticEmail(t *testing.T) {
	store, enrollment, ctx := setupEnrollment(t, nil)
	input := enrollmentInput()
	input.Email = identity.SyntheticEmail(input.Subject, "tenant-1", "feishu.local")
	input.EmailSynthetic = true
	result, err := enrollment.Enroll(ctx, input)
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	wantEmail := "on_member@tenant-1.feishu.local"
	if result.User.Email != wantEmail {
		t.Fatalf("synthetic email = %q, want %q", result.User.Email, wantEmail)
	}
	identity, err := store.GetLoginIdentityByProvider(ctx, "feishu", input.Subject)
	if err != nil {
		t.Fatal(err)
	}
	if identity.RawClaims["email_synthetic"] != true {
		t.Fatalf("email_synthetic claim = %#v", identity.RawClaims["email_synthetic"])
	}
}

func TestAccountEnrollmentUsesRequestNamespace(t *testing.T) {
	store, enrollment, ctx := setupEnrollment(t, nil)
	input := auth.AccountEnrollmentInput{
		Namespace: "partner",
		Subject:   "partner-subject",
		Email:     "partner@example.test",
		Name:      "Partner Member",
	}
	result, err := enrollment.Enroll(ctx, input)
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	login, err := store.GetLoginIdentityByProvider(ctx, input.Namespace, input.Subject)
	if err != nil {
		t.Fatalf("get login identity: %v", err)
	}
	if login.UserID != result.User.ID || login.Provider != input.Namespace {
		t.Fatalf("login identity = %+v, want user %s in namespace %q", login, result.User.ID, input.Namespace)
	}
	channel, err := store.GetChannelIdentityByPlatform(ctx, input.Namespace, input.Subject)
	if err != nil {
		t.Fatalf("get channel identity: %v", err)
	}
	if channel.UserID != result.User.ID || channel.Platform != input.Namespace {
		t.Fatalf("channel identity = %+v, want user %s in namespace %q", channel, result.User.ID, input.Namespace)
	}
}

func TestFeishuEnrollmentCompletesExistingIdentity(t *testing.T) {
	for _, kind := range []string{"login", "channel"} {
		t.Run(kind, func(t *testing.T) {
			store, enrollment, ctx := setupEnrollment(t, nil)
			user, err := store.CreateUser(ctx, auth.User{ID: uuid.NewString(), Email: "member@example.test", Role: auth.RoleUser})
			if err != nil {
				t.Fatal(err)
			}
			if kind == "login" {
				_, err = store.CreateLoginIdentity(ctx, auth.LoginIdentity{ID: uuid.NewString(), UserID: user.ID, Provider: "feishu", ProviderSubject: "on_member"})
			} else {
				_, err = store.CreateChannelIdentity(ctx, auth.ChannelIdentity{ID: uuid.NewString(), UserID: user.ID, Platform: "feishu", ExternalID: "on_member"})
			}
			if err != nil {
				t.Fatal(err)
			}
			result, err := enrollment.Enroll(ctx, enrollmentInput())
			if err != nil {
				t.Fatalf("Enroll: %v", err)
			}
			if result.Created || result.User.ID != user.ID {
				t.Fatalf("result = %+v, want existing user %s", result, user.ID)
			}
			if users, logins, channels := enrollmentCounts(t, store); users != 2 || logins != 1 || channels != 1 {
				t.Fatalf("row counts = %d:%d:%d, want 2:1:1", users, logins, channels)
			}
		})
	}
}

func TestFeishuEnrollmentRejectsNoAdminInactiveAndConflictsWithoutWrites(t *testing.T) {
	t.Run("no active admin", func(t *testing.T) {
		pool := newTestDB(t)
		store := NewOIDCStore(pool)
		enrollment := auth.NewAccountEnrollmentService(store, nil)
		_, err := enrollment.Enroll(t.Context(), enrollmentInput())
		if !errors.Is(err, auth.ErrEnrollmentNoActiveAdmin) {
			t.Fatalf("Enroll error = %v", err)
		}
		if users, logins, channels := enrollmentCounts(t, store); users != 0 || logins != 0 || channels != 0 {
			t.Fatalf("row counts = %d:%d:%d, want 0:0:0", users, logins, channels)
		}
	})

	t.Run("inactive matching user", func(t *testing.T) {
		store, enrollment, ctx := setupEnrollment(t, nil)
		user, err := store.CreateUser(ctx, auth.User{ID: uuid.NewString(), Email: "member@example.test", Role: auth.RoleUser})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.UpdateUserActive(ctx, user.ID, false); err != nil {
			t.Fatal(err)
		}
		if _, err := store.CreateLoginIdentity(ctx, auth.LoginIdentity{ID: uuid.NewString(), UserID: user.ID, Provider: "feishu", ProviderSubject: "on_member"}); err != nil {
			t.Fatal(err)
		}
		before := enrollmentCountsSnapshot(t, store)
		_, err = enrollment.Enroll(ctx, enrollmentInput())
		if !errors.Is(err, auth.ErrEnrollmentInactiveUser) {
			t.Fatalf("Enroll error = %v", err)
		}
		if got := enrollmentCountsSnapshot(t, store); got != before {
			t.Fatalf("row counts changed from %v to %v", before, got)
		}
	})

	t.Run("cross-user identities", func(t *testing.T) {
		store, enrollment, ctx := setupEnrollment(t, nil)
		first, err := store.CreateUser(ctx, auth.User{ID: uuid.NewString(), Email: "first@example.test", Role: auth.RoleUser})
		if err != nil {
			t.Fatal(err)
		}
		second, err := store.CreateUser(ctx, auth.User{ID: uuid.NewString(), Email: "second@example.test", Role: auth.RoleUser})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.CreateLoginIdentity(ctx, auth.LoginIdentity{ID: uuid.NewString(), UserID: first.ID, Provider: "feishu", ProviderSubject: "on_member"}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.CreateChannelIdentity(ctx, auth.ChannelIdentity{ID: uuid.NewString(), UserID: second.ID, Platform: "feishu", ExternalID: "on_member"}); err != nil {
			t.Fatal(err)
		}
		before := enrollmentCountsSnapshot(t, store)
		_, err = enrollment.Enroll(ctx, enrollmentInput())
		if !errors.Is(err, auth.ErrEnrollmentIdentityConflict) {
			t.Fatalf("Enroll error = %v", err)
		}
		if got := enrollmentCountsSnapshot(t, store); got != before {
			t.Fatalf("row counts changed from %v to %v", before, got)
		}
	})

	t.Run("email owned by another user", func(t *testing.T) {
		store, enrollment, ctx := setupEnrollment(t, nil)
		if _, err := store.CreateUser(ctx, auth.User{ID: uuid.NewString(), Email: "member@example.test", Role: auth.RoleUser}); err != nil {
			t.Fatal(err)
		}
		before := enrollmentCountsSnapshot(t, store)
		_, err := enrollment.Enroll(ctx, enrollmentInput())
		if !errors.Is(err, auth.ErrEnrollmentEmailConflict) {
			t.Fatalf("Enroll error = %v", err)
		}
		if got := enrollmentCountsSnapshot(t, store); got != before {
			t.Fatalf("row counts changed from %v to %v", before, got)
		}
	})

	t.Run("identity user email differs", func(t *testing.T) {
		store, enrollment, ctx := setupEnrollment(t, nil)
		identityUser, err := store.CreateUser(ctx, auth.User{ID: uuid.NewString(), Email: "identity@example.test", Role: auth.RoleUser})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.CreateLoginIdentity(ctx, auth.LoginIdentity{ID: uuid.NewString(), UserID: identityUser.ID, Provider: "feishu", ProviderSubject: "on_member"}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.CreateUser(ctx, auth.User{ID: uuid.NewString(), Email: "member@example.test", Role: auth.RoleUser}); err != nil {
			t.Fatal(err)
		}
		before := enrollmentCountsSnapshot(t, store)
		_, err = enrollment.Enroll(ctx, enrollmentInput())
		if !errors.Is(err, auth.ErrEnrollmentEmailConflict) {
			t.Fatalf("Enroll error = %v", err)
		}
		if got := enrollmentCountsSnapshot(t, store); got != before {
			t.Fatalf("row counts changed from %v to %v", before, got)
		}
	})
}

func TestFeishuEnrollmentRollsBackOnIdentityWriteFailure(t *testing.T) {
	store, enrollment, ctx := setupEnrollment(t, nil)
	if _, err := store.db.Exec(ctx, `CREATE FUNCTION fail_feishu_channel_identity() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'forced channel identity failure'; END $$`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(ctx, `CREATE TRIGGER fail_feishu_channel_identity BEFORE INSERT ON channel_identity FOR EACH ROW EXECUTE FUNCTION fail_feishu_channel_identity()`); err != nil {
		t.Fatal(err)
	}
	_, err := enrollment.Enroll(ctx, enrollmentInput())
	if err == nil {
		t.Fatal("Enroll succeeded despite channel identity failure")
	}
	if users, logins, channels := enrollmentCounts(t, store); users != 1 || logins != 0 || channels != 0 {
		t.Fatalf("row counts = %d:%d:%d, want 1:0:0 after rollback", users, logins, channels)
	}
}

func TestFeishuEnrollmentIsIdempotentAndConcurrent(t *testing.T) {
	_, enrollment, ctx := setupEnrollment(t, nil)
	first, err := enrollment.Enroll(ctx, enrollmentInput())
	if err != nil {
		t.Fatalf("first Enroll: %v", err)
	}
	second, err := enrollment.Enroll(ctx, enrollmentInput())
	if err != nil {
		t.Fatalf("second Enroll: %v", err)
	}
	if second.Created || second.User.ID != first.User.ID {
		t.Fatalf("second result = %+v", second)
	}

	store, enrollment, ctx := setupEnrollment(t, nil)
	const callers = 8
	results := make(chan auth.AccountEnrollmentResult, callers)
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Go(func() {
			result, err := enrollment.Enroll(ctx, enrollmentInput())
			results <- result
			errs <- err
		})
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Enroll: %v", err)
		}
	}
	var userID string
	for result := range results {
		if userID == "" {
			userID = result.User.ID
		}
		if result.User.ID != userID {
			t.Fatalf("concurrent user ID = %s, want %s", result.User.ID, userID)
		}
	}
	if users, logins, channels := enrollmentCounts(t, store); users != 2 || logins != 1 || channels != 1 {
		t.Fatalf("row counts = %d:%d:%d, want 2:1:1", users, logins, channels)
	}
}

type blockingChannelIdentityStore struct {
	auth.ChannelIdentityStore
	block func()
}

func (s blockingChannelIdentityStore) GetChannelIdentityByPlatform(ctx context.Context, platform, externalID string) (auth.ChannelIdentity, error) {
	identity, err := s.ChannelIdentityStore.GetChannelIdentityByPlatform(ctx, platform, externalID)
	s.block()
	return identity, err
}

type blockingChannelEnrollmentTransactioner struct {
	store   *OIDCStore
	checked chan<- struct{}
	release <-chan struct{}
	once    sync.Once
}

func (t *blockingChannelEnrollmentTransactioner) BeginAuthTx(ctx context.Context) (auth.AuthStores, func() error, func(), error) {
	stores, commit, rollback, err := t.store.BeginAuthTx(ctx)
	if err == nil {
		stores.Channels = blockingChannelIdentityStore{
			ChannelIdentityStore: stores.Channels,
			block: func() {
				t.once.Do(func() {
					close(t.checked)
					<-t.release
				})
			},
		}
	}
	return stores, commit, rollback, err
}

func TestFeishuEnrollmentRetriesWhenPeerCommitsAfterIdentityLookup(t *testing.T) {
	store, peer, ctx := setupEnrollment(t, nil)
	checked := make(chan struct{})
	release := make(chan struct{})
	enrollment := auth.NewAccountEnrollmentService(&blockingChannelEnrollmentTransactioner{
		store: store, checked: checked, release: release,
	}, nil)

	result := make(chan auth.AccountEnrollmentResult, 1)
	errs := make(chan error, 1)
	go func() {
		enrolled, err := enrollment.Enroll(ctx, enrollmentInput())
		result <- enrolled
		errs <- err
	}()

	<-checked
	peerResult, err := peer.Enroll(ctx, enrollmentInput())
	close(release)
	if err != nil {
		t.Fatalf("peer Enroll: %v", err)
	}
	if err := <-errs; err != nil {
		t.Fatalf("concurrent Enroll: %v", err)
	}
	if enrolled := <-result; enrolled.User.ID != peerResult.User.ID {
		t.Fatalf("concurrent user ID = %s, want %s", enrolled.User.ID, peerResult.User.ID)
	}
}

type blockingActiveUserStore struct {
	auth.ActiveUserStore
	locked  chan<- struct{}
	release <-chan struct{}
}

func (s blockingActiveUserStore) GetActiveUserForShare(ctx context.Context, userID string) (auth.User, error) {
	user, err := s.ActiveUserStore.GetActiveUserForShare(ctx, userID)
	close(s.locked)
	<-s.release
	return user, err
}

type blockingEnrollmentTransactioner struct {
	store   *OIDCStore
	locked  chan<- struct{}
	release <-chan struct{}
}

func (t blockingEnrollmentTransactioner) BeginAuthTx(ctx context.Context) (auth.AuthStores, func() error, func(), error) {
	stores, commit, rollback, err := t.store.BeginAuthTx(ctx)
	if err == nil {
		stores.ActiveUsers = blockingActiveUserStore{
			ActiveUserStore: stores.ActiveUsers,
			locked:          t.locked,
			release:         t.release,
		}
	}
	return stores, commit, rollback, err
}

func TestFeishuEnrollmentSerializesExistingUserDeactivation(t *testing.T) {
	store, _, ctx := setupEnrollment(t, nil)
	user, err := store.CreateUser(ctx, auth.User{ID: uuid.NewString(), Email: "member@example.test", Role: auth.RoleUser})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateLoginIdentity(ctx, auth.LoginIdentity{ID: uuid.NewString(), UserID: user.ID, Provider: "feishu", ProviderSubject: "on_member"}); err != nil {
		t.Fatal(err)
	}

	locked := make(chan struct{})
	release := make(chan struct{})
	enrollment := auth.NewAccountEnrollmentService(blockingEnrollmentTransactioner{
		store: store, locked: locked, release: release,
	}, nil)
	enrolled := make(chan error, 1)
	go func() {
		_, err := enrollment.Enroll(ctx, enrollmentInput())
		enrolled <- err
	}()
	<-locked

	deactivated := make(chan error, 1)
	go func() {
		deactivated <- store.UpdateUserActive(ctx, user.ID, false)
	}()
	premature := false
	select {
	case err := <-deactivated:
		premature = true
		if err != nil {
			t.Errorf("premature deactivation: %v", err)
		}
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err := <-enrolled; err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	if !premature {
		if err := <-deactivated; err != nil {
			t.Fatalf("deactivate: %v", err)
		}
	}
	if premature {
		t.Fatal("deactivation completed while enrollment held the active-user lock")
	}
	if _, err := store.GetChannelIdentityByPlatform(ctx, "feishu", "on_member"); err != nil {
		t.Fatalf("completed channel identity: %v", err)
	}
}

type enrollmentCountSnapshot struct{ users, logins, channels int }

func enrollmentCountsSnapshot(t *testing.T, store *OIDCStore) enrollmentCountSnapshot {
	users, logins, channels := enrollmentCounts(t, store)
	return enrollmentCountSnapshot{users, logins, channels}
}
