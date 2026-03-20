package auth_test

import (
	"testing"

	"github.com/vaayne/anna/internal/auth"
)

func TestHashPassword(t *testing.T) {
	t.Parallel()

	plain := "securepassword123"
	hash, err := auth.HashPassword(plain)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if hash == "" {
		t.Fatal("HashPassword returned empty hash")
	}
	if hash == plain {
		t.Fatal("HashPassword returned plaintext")
	}

	// Hashing the same password twice should produce different hashes (bcrypt salt).
	hash2, err := auth.HashPassword(plain)
	if err != nil {
		t.Fatalf("HashPassword (second call): %v", err)
	}
	if hash == hash2 {
		t.Error("two calls to HashPassword produced identical hashes")
	}
}

func TestCheckPassword(t *testing.T) {
	t.Parallel()

	plain := "securepassword123"
	hash, err := auth.HashPassword(plain)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}

	if err := auth.CheckPassword(hash, plain); err != nil {
		t.Errorf("CheckPassword with correct password: %v", err)
	}

	if err := auth.CheckPassword(hash, "wrongpassword"); err == nil {
		t.Error("CheckPassword with wrong password should return error")
	}
}

func TestCheckPasswordEmptyHash(t *testing.T) {
	t.Parallel()

	if err := auth.CheckPassword("", "anything"); err == nil {
		t.Error("CheckPassword with empty hash should return error")
	}
}
