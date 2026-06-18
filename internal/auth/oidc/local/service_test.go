package local

import (
	"context"
	"errors"
	"testing"

	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/db/dbtest"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

func newTestService(t *testing.T, cfg *Config) (*Service, *appdb.OIDCStore) {
	t.Helper()
	db := dbtest.New(t)
	store := appdb.NewOIDCStore(db)
	return NewService(cfg, store, store), store
}

func registerTestUser(t *testing.T, svc *Service, email string) string {
	t.Helper()
	id, err := svc.Register(context.Background(), RegisterInput{
		Name:            email,
		Email:           email,
		Password:        "password123",
		ConfirmPassword: "password123",
	})
	if err != nil {
		t.Fatalf("Register(%q): %v", email, err)
	}
	return id
}

func TestServiceBootstrapRegistrationClosesAfterFirstUser(t *testing.T) {
	svc, store := newTestService(t, &Config{BootstrapRegistration: true})
	ctx := context.Background()

	if !svc.AllowsRegistration(ctx) {
		t.Fatal("expected bootstrap registration to be allowed on empty DB")
	}

	firstID := registerTestUser(t, svc, "first@example.com")
	first, err := store.GetUser(ctx, firstID)
	if err != nil {
		t.Fatalf("GetUser(first): %v", err)
	}
	if first.Role != "admin" {
		t.Fatalf("first role = %q, want admin", first.Role)
	}
	if svc.AllowsRegistration(ctx) {
		t.Fatal("expected registration to close after bootstrap user")
	}

	_, err = svc.Register(ctx, RegisterInput{
		Name:            "second",
		Email:           "second@example.com",
		Password:        "password123",
		ConfirmPassword: "password123",
	})
	if !errors.Is(err, ErrRegistrationDisabled) {
		t.Fatalf("second Register error = %v, want ErrRegistrationDisabled", err)
	}
}

func TestServiceExplicitRegistrationStaysOpen(t *testing.T) {
	svc, store := newTestService(t, &Config{AllowRegistration: true, BootstrapRegistration: true})
	ctx := context.Background()

	registerTestUser(t, svc, "first@example.com")
	secondID := registerTestUser(t, svc, "second@example.com")
	second, err := store.GetUser(ctx, secondID)
	if err != nil {
		t.Fatalf("GetUser(second): %v", err)
	}
	if second.Role != "user" {
		t.Fatalf("second role = %q, want user", second.Role)
	}
	if !svc.AllowsRegistration(ctx) {
		t.Fatal("expected explicit registration to stay open")
	}
}

func TestServiceDisabledUserRequiresValidPassword(t *testing.T) {
	svc, store := newTestService(t, &Config{AllowRegistration: true, BootstrapRegistration: true})
	ctx := context.Background()
	userID := registerTestUser(t, svc, "disabled@example.com")
	if err := store.UpdateUserActive(ctx, userID, false); err != nil {
		t.Fatalf("UpdateUserActive: %v", err)
	}

	_, err := svc.Login(ctx, LoginInput{Email: "disabled@example.com", Password: "wrong-password"})
	if !errors.Is(err, ErrInvalidLogin) {
		t.Fatalf("wrong password error = %v, want ErrInvalidLogin", err)
	}

	_, err = svc.Login(ctx, LoginInput{Email: "disabled@example.com", Password: "password123"})
	if !errors.Is(err, ErrAccountDisabled) {
		t.Fatalf("valid password error = %v, want ErrAccountDisabled", err)
	}
}
