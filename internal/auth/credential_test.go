package auth_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/auth"
)

// fakeCredentialStore is an in-memory CredentialStore for unit tests.
type fakeCredentialStore struct {
	byUser map[string]auth.Credential
}

func newFakeCredentialStore() *fakeCredentialStore {
	return &fakeCredentialStore{byUser: make(map[string]auth.Credential)}
}

func (f *fakeCredentialStore) CreateCredential(_ context.Context, c auth.Credential) (auth.Credential, error) {
	if _, ok := f.byUser[c.UserID]; ok {
		return auth.Credential{}, errors.New("already exists")
	}
	f.byUser[c.UserID] = c
	return c, nil
}

func (f *fakeCredentialStore) GetCredentialByUserID(_ context.Context, userID string) (auth.Credential, error) {
	c, ok := f.byUser[userID]
	if !ok {
		return auth.Credential{}, auth.ErrNotFound
	}
	return c, nil
}

func (f *fakeCredentialStore) UpdateCredentialHash(_ context.Context, userID, hash string) error {
	c, ok := f.byUser[userID]
	if !ok {
		return auth.ErrNotFound
	}
	c.PasswordHash = hash
	f.byUser[userID] = c
	return nil
}

func (f *fakeCredentialStore) DeleteCredential(_ context.Context, userID string) error {
	delete(f.byUser, userID)
	return nil
}

func TestCredentialServiceSetAndVerify(t *testing.T) {
	svc := auth.NewCredentialService(newFakeCredentialStore())
	ctx := context.Background()
	userID := uuid.NewString()

	if err := svc.SetPassword(ctx, userID, "correct-password"); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}

	if err := svc.VerifyPassword(ctx, userID, "correct-password"); err != nil {
		t.Fatalf("VerifyPassword correct: %v", err)
	}
}

func TestCredentialServiceWrongPassword(t *testing.T) {
	svc := auth.NewCredentialService(newFakeCredentialStore())
	ctx := context.Background()
	userID := uuid.NewString()

	if err := svc.SetPassword(ctx, userID, "correct"); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}

	err := svc.VerifyPassword(ctx, userID, "wrong")
	if !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Errorf("got %v, want ErrInvalidCredentials", err)
	}
}

func TestCredentialServiceVerifyNotFound(t *testing.T) {
	svc := auth.NewCredentialService(newFakeCredentialStore())
	ctx := context.Background()

	err := svc.VerifyPassword(ctx, "no-such-user", "any")
	if !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("got %v, want ErrNotFound", err)
	}
}

func TestCredentialServiceSetPasswordUpdatesExisting(t *testing.T) {
	store := newFakeCredentialStore()
	svc := auth.NewCredentialService(store)
	ctx := context.Background()
	userID := uuid.NewString()

	if err := svc.SetPassword(ctx, userID, "first"); err != nil {
		t.Fatalf("SetPassword first: %v", err)
	}
	if err := svc.SetPassword(ctx, userID, "second"); err != nil {
		t.Fatalf("SetPassword second: %v", err)
	}

	if err := svc.VerifyPassword(ctx, userID, "second"); err != nil {
		t.Fatalf("VerifyPassword second: %v", err)
	}
	if err := svc.VerifyPassword(ctx, userID, "first"); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Errorf("old password should be rejected, got %v", err)
	}
}

func TestCredentialServiceHasCredential(t *testing.T) {
	svc := auth.NewCredentialService(newFakeCredentialStore())
	ctx := context.Background()
	userID := uuid.NewString()

	has, err := svc.HasCredential(ctx, userID)
	if err != nil || has {
		t.Errorf("HasCredential before set: has=%v err=%v", has, err)
	}

	if err := svc.SetPassword(ctx, userID, "pw"); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}

	has, err = svc.HasCredential(ctx, userID)
	if err != nil || !has {
		t.Errorf("HasCredential after set: has=%v err=%v", has, err)
	}
}
