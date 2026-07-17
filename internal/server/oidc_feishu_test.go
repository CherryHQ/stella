package server

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/auth/account"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/db/dbtest"
)

func TestLinkFeishuChannelIdentityFromLogin(t *testing.T) {
	db := dbtest.New(t)
	store := appdb.NewOIDCStore(db)
	user, err := store.CreateUser(context.Background(), auth.User{
		ID:    uuid.NewString(),
		Email: "feishu@example.com",
		Name:  "Feishu User",
		Role:  auth.RoleUser,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	srv := &Server{account: account.NewService(store, store, store, store, store, nil, nil, nil)}
	srv.linkFeishuChannelIdentity(context.Background(), user.ID, auth.ExternalIdentity{
		Provider: "feishu",
		Subject:  "on_union",
		Name:     "Feishu User",
	})

	identity, err := store.GetChannelIdentityByPlatform(context.Background(), "feishu", "on_union")
	if err != nil {
		t.Fatalf("GetChannelIdentityByPlatform: %v", err)
	}
	if identity.UserID != user.ID || identity.Name != "Feishu User" {
		t.Fatalf("identity = %+v, want user %s", identity, user.ID)
	}
}

func TestLinkFeishuChannelIdentityDoesNotOverrideExistingUser(t *testing.T) {
	db := dbtest.New(t)
	store := appdb.NewOIDCStore(db)
	owner, err := store.CreateUser(context.Background(), auth.User{
		ID:    uuid.NewString(),
		Email: "owner@example.com",
		Name:  "Owner",
		Role:  auth.RoleUser,
	})
	if err != nil {
		t.Fatalf("CreateUser(owner): %v", err)
	}
	loginUser, err := store.CreateUser(context.Background(), auth.User{
		ID:    uuid.NewString(),
		Email: "login@example.com",
		Name:  "Login",
		Role:  auth.RoleUser,
	})
	if err != nil {
		t.Fatalf("CreateUser(login): %v", err)
	}
	if _, err := store.CreateChannelIdentity(context.Background(), auth.ChannelIdentity{
		ID:         uuid.NewString(),
		UserID:     owner.ID,
		Platform:   "feishu",
		ExternalID: "on_union",
		Name:       "Owner",
	}); err != nil {
		t.Fatalf("CreateChannelIdentity: %v", err)
	}

	srv := &Server{account: account.NewService(store, store, store, store, store, nil, nil, nil)}
	srv.linkFeishuChannelIdentity(context.Background(), loginUser.ID, auth.ExternalIdentity{
		Provider: "feishu",
		Subject:  "on_union",
		Name:     "Login",
	})

	identity, err := store.GetChannelIdentityByPlatform(context.Background(), "feishu", "on_union")
	if err != nil {
		t.Fatalf("GetChannelIdentityByPlatform: %v", err)
	}
	if identity.UserID != owner.ID {
		t.Fatalf("identity user = %q, want existing owner %q", identity.UserID, owner.ID)
	}
}
