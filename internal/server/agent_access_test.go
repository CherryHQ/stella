package server

import (
	"testing"

	"github.com/CherryHQ/stella/internal/auth"
)

func TestAuthInfoAuthorityUsesEffectiveAdminGate(t *testing.T) {
	t.Run("bearer for admin account stays non-admin", func(t *testing.T) {
		authority, err := (&AuthInfo{UserID: "user-1", Role: auth.RoleAdmin, IsAdmin: false}).authority()
		if err != nil {
			t.Fatal(err)
		}
		if authority.IsAdmin() {
			t.Fatal("account role bypassed the bearer credential's non-admin gate")
		}
	})

	t.Run("verified admin session remains admin", func(t *testing.T) {
		authority, err := (&AuthInfo{UserID: "user-1", Role: auth.RoleUser, IsAdmin: true}).authority()
		if err != nil {
			t.Fatal(err)
		}
		if !authority.IsAdmin() {
			t.Fatal("verified admin gate was dropped")
		}
	})
}
