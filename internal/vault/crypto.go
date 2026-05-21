package vault

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	"filippo.io/age"
	"filippo.io/age/armor"
)

func GenerateMasterIdentity() (string, error) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		return "", fmt.Errorf("vault: generate master identity: %w", err)
	}
	return id.String(), nil
}

// ParseMasterIdentity parses an age identity string (e.g. "AGE-SECRET-KEY-1...")
// and returns both the identity (for decryption) and its recipient (for encryption).
func ParseMasterIdentity(identityStr string) (*age.X25519Identity, *age.X25519Recipient, error) {
	id, err := age.ParseX25519Identity(strings.TrimSpace(identityStr))
	if err != nil {
		return nil, nil, fmt.Errorf("vault: parse master identity: %w", err)
	}
	return id, id.Recipient(), nil
}

// GenerateUserKeys creates a new X25519 keypair for a user.
// The private key is encrypted with masterRecipient before returning.
// Returns (publicKeyString, encryptedPrivateKey, error).
// publicKeyString is the age public key string (age1...).
// encryptedPrivateKey is the armored age-encrypted private key.
func GenerateUserKeys(masterRecipient *age.X25519Recipient) (string, string, error) {
	userID, err := age.GenerateX25519Identity()
	if err != nil {
		return "", "", fmt.Errorf("vault: generate user keys: %w", err)
	}

	encPrivKey, err := encryptArmored(masterRecipient, userID.String())
	if err != nil {
		return "", "", fmt.Errorf("vault: encrypt user private key: %w", err)
	}

	return userID.Recipient().String(), encPrivKey, nil
}

// Encrypt encrypts plaintext using the given public key string.
// Returns armored ciphertext.
func Encrypt(publicKeyStr string, plaintext string) (string, error) {
	recipient, err := age.ParseX25519Recipient(strings.TrimSpace(publicKeyStr))
	if err != nil {
		return "", fmt.Errorf("vault: parse public key: %w", err)
	}

	ciphertext, err := encryptArmored(recipient, plaintext)
	if err != nil {
		return "", fmt.Errorf("vault: encrypt: %w", err)
	}

	return ciphertext, nil
}

// Decrypt decrypts ciphertext that was encrypted with a user's public key.
// First decrypts the user's private key using the master identity,
// then uses that private key to decrypt the ciphertext.
// masterIdentity: the server's master age identity
// encryptedPrivateKey: the user's age private key, encrypted with master
// ciphertext: the age-encrypted secret value
func Decrypt(masterIdentity *age.X25519Identity, encryptedPrivateKey string, ciphertext string) (string, error) {
	// Decrypt the user's private key using the master identity.
	privKeyStr, err := decryptArmored(masterIdentity, encryptedPrivateKey)
	if err != nil {
		return "", fmt.Errorf("vault: decrypt user private key: %w", err)
	}

	// Parse the recovered private key.
	userID, err := age.ParseX25519Identity(strings.TrimSpace(privKeyStr))
	if err != nil {
		return "", fmt.Errorf("vault: parse user private key: %w", err)
	}

	// Decrypt the actual ciphertext with the user's private key.
	plaintext, err := decryptArmored(userID, ciphertext)
	if err != nil {
		return "", fmt.Errorf("vault: decrypt ciphertext: %w", err)
	}

	return plaintext, nil
}

// encryptArmored encrypts plaintext with recipient and returns armored ciphertext.
func encryptArmored(recipient age.Recipient, plaintext string) (string, error) {
	var buf bytes.Buffer
	armorWriter := armor.NewWriter(&buf)

	w, err := age.Encrypt(armorWriter, recipient)
	if err != nil {
		return "", err
	}
	if _, err := io.WriteString(w, plaintext); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	if err := armorWriter.Close(); err != nil {
		return "", err
	}

	return buf.String(), nil
}

// decryptArmored decrypts armored ciphertext using identity and returns plaintext.
func decryptArmored(identity age.Identity, ciphertext string) (string, error) {
	armorReader := armor.NewReader(strings.NewReader(ciphertext))
	r, err := age.Decrypt(armorReader, identity)
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		return "", err
	}

	return buf.String(), nil
}
