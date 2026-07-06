package oidc

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"

	"golang.org/x/crypto/bcrypt"

	"github.com/CherryHQ/stella/internal/credential"
)

// Refresh tokens share the opaque wire format with credential.MintOpaque
// (prefix + public_id + "_" + secret + crc32, SHA-256 hashed). The format lives
// in credential; here we only choose the refresh prefix and orchestrate rotation
// -- refresh tokens never reach the API front door, only /oauth/token.
const refreshPrefix = credential.OAuthRefreshPrefix

// mintRefreshToken mints an opaque stella_ort_ refresh token.
func mintRefreshToken() (credential.Minted, error) {
	return credential.MintOpaqueWithPrefix(refreshPrefix)
}

// parseRefreshToken splits and checksum-verifies a stella_ort_ token, returning
// the public id (lookup key) and the secret (hashed and compared to storage).
func parseRefreshToken(raw string) (publicID, secret string, err error) {
	return credential.ParseOpaqueToken(refreshPrefix, raw)
}

// hashCode is the SHA-256 of an authorization code (stored as code_hash).
func hashCode(code string) string { return credential.HashSecret(code) }

// generateClientID mints a public, non-secret client identifier.
func generateClientID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("oauth: generate client id: %w", err)
	}
	return "stella_client_" + hex.EncodeToString(raw), nil
}

// generateClientSecret mints a high-entropy client secret (shown once).
func generateClientSecret() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("oauth: generate client secret: %w", err)
	}
	return "stella_cs_" + base64.RawURLEncoding.EncodeToString(raw), nil
}

// hashClientSecret hashes a client secret with bcrypt. Unlike the high-entropy
// opaque tokens (SHA-256), a client_secret is a password-like shared secret, so
// a slow password hasher is the correct choice.
func hashClientSecret(secret string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(secret), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("oauth: hash client secret: %w", err)
	}
	return string(h), nil
}

// verifyClientSecret reports whether a presented secret matches the stored hash.
func verifyClientSecret(hash, secret string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(secret)) == nil
}
