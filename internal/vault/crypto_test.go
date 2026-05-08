package vault_test

import (
	"testing"

	"filippo.io/age"

	"github.com/CherryHQ/stella/internal/vault"
)

func TestParsesMasterIdentity(t *testing.T) {
	generated, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("GenerateX25519Identity: %v", err)
	}

	id, recipient, err := vault.ParseMasterIdentity(generated.String())
	if err != nil {
		t.Fatalf("ParseMasterIdentity: %v", err)
	}

	if id.Recipient().String() != generated.Recipient().String() {
		t.Errorf("recipient mismatch: got %q want %q", id.Recipient().String(), generated.Recipient().String())
	}
	if recipient.String() != generated.Recipient().String() {
		t.Errorf("returned recipient mismatch: got %q want %q", recipient.String(), generated.Recipient().String())
	}
}

func TestGenerateUserKeysRoundTrip(t *testing.T) {
	masterID, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("GenerateX25519Identity: %v", err)
	}
	masterRecipient := masterID.Recipient()

	pubKey, encPrivKey, err := vault.GenerateUserKeys(masterRecipient)
	if err != nil {
		t.Fatalf("GenerateUserKeys: %v", err)
	}
	if pubKey == "" {
		t.Fatal("pubKey is empty")
	}
	if encPrivKey == "" {
		t.Fatal("encPrivKey is empty")
	}

	const secret = "hello from user keys"
	ciphertext, err := vault.Encrypt(pubKey, secret)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	plaintext, err := vault.Decrypt(masterID, encPrivKey, ciphertext)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}

	if plaintext != secret {
		t.Errorf("round-trip mismatch: got %q want %q", plaintext, secret)
	}
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	masterID, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("GenerateX25519Identity: %v", err)
	}

	pubKey, encPrivKey, err := vault.GenerateUserKeys(masterID.Recipient())
	if err != nil {
		t.Fatalf("GenerateUserKeys: %v", err)
	}

	const secret = "super secret value"
	ciphertext, err := vault.Encrypt(pubKey, secret)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	plaintext, err := vault.Decrypt(masterID, encPrivKey, ciphertext)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}

	if plaintext != secret {
		t.Errorf("decrypt mismatch: got %q want %q", plaintext, secret)
	}
}

func TestDecryptWrongMasterKeyFails(t *testing.T) {
	masterID, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("GenerateX25519Identity: %v", err)
	}

	pubKey, encPrivKey, err := vault.GenerateUserKeys(masterID.Recipient())
	if err != nil {
		t.Fatalf("GenerateUserKeys: %v", err)
	}

	ciphertext, err := vault.Encrypt(pubKey, "secret")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	wrongID, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("GenerateX25519Identity (wrong): %v", err)
	}

	_, err = vault.Decrypt(wrongID, encPrivKey, ciphertext)
	if err == nil {
		t.Fatal("expected error when decrypting with wrong master key, got nil")
	}
}

func TestEncryptInvalidPublicKey(t *testing.T) {
	_, err := vault.Encrypt("not-a-valid-age-public-key", "secret")
	if err == nil {
		t.Fatal("expected error for invalid public key, got nil")
	}
}
