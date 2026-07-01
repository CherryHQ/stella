package oauth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"hash/crc32"
	"strings"

	"golang.org/x/crypto/bcrypt"

	"github.com/CherryHQ/stella/internal/credential"
)

// Refresh tokens reuse the same opaque token shape as credential.MintOpaque
// (prefix + public_id + "_" + secret + crc32). credential deliberately does NOT
// mint refresh tokens -- they never reach the API front door, only /oauth/token
// -- so their rotation lives here, consistent with the anti-scatter boundary.
const (
	refreshPrefix    = credential.OAuthRefreshPrefix // stella_ort_
	publicIDBytes    = 12
	secretBytes      = 32
	secretEncodedLen = 43 // base64url(32 bytes), no padding
	crcLen           = 8
)

type mintedRefresh struct {
	Plaintext string
	PublicID  string
	TokenHash string
	Last4     string
}

// mintRefreshToken mints an opaque stella_ort_ refresh token.
func mintRefreshToken() (mintedRefresh, error) {
	pubRaw := make([]byte, publicIDBytes)
	if _, err := rand.Read(pubRaw); err != nil {
		return mintedRefresh{}, fmt.Errorf("oauth: generate refresh public id: %w", err)
	}
	secRaw := make([]byte, secretBytes)
	if _, err := rand.Read(secRaw); err != nil {
		return mintedRefresh{}, fmt.Errorf("oauth: generate refresh secret: %w", err)
	}
	publicID := hex.EncodeToString(pubRaw)
	secret := base64.RawURLEncoding.EncodeToString(secRaw)
	body := refreshPrefix + publicID + "_" + secret
	plaintext := body + checksum(body)
	return mintedRefresh{
		Plaintext: plaintext,
		PublicID:  publicID,
		TokenHash: hashSecret(secret),
		Last4:     lastN(plaintext, 4),
	}, nil
}

// parseRefreshToken splits and checksum-verifies a stella_ort_ token, returning
// the public id (lookup key) and the secret (hashed and compared to storage).
func parseRefreshToken(raw string) (publicID, secret string, err error) {
	if !strings.HasPrefix(raw, refreshPrefix) {
		return "", "", fmt.Errorf("oauth: not a refresh token")
	}
	rest := strings.TrimPrefix(raw, refreshPrefix)
	publicID, tail, ok := strings.Cut(rest, "_")
	if !ok || publicID == "" || len(tail) != secretEncodedLen+crcLen {
		return "", "", fmt.Errorf("oauth: malformed refresh token")
	}
	secret = tail[:secretEncodedLen]
	crc := tail[secretEncodedLen:]
	body := refreshPrefix + publicID + "_" + secret
	if crc != checksum(body) {
		return "", "", fmt.Errorf("oauth: refresh token checksum mismatch")
	}
	return publicID, secret, nil
}

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
	raw := make([]byte, secretBytes)
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

func hashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// hashCode is the SHA-256 of an authorization code (stored as code_hash).
func hashCode(code string) string {
	return hashSecret(code)
}

func checksum(body string) string {
	sum := crc32.ChecksumIEEE([]byte(body))
	return fmt.Sprintf("%0*x", crcLen, sum)
}

func lastN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
