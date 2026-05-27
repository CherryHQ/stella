package oidc

import (
	"crypto/sha256"
	"encoding/base64"
)

// pkceChallenge computes the S256 PKCE code challenge from a raw code verifier.
// Returns (challenge, method, error). The challenge is base64url-encoded SHA-256
// of the verifier; method is always "S256".
func pkceChallenge(verifier string) (challenge, method string, err error) {
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return challenge, "S256", nil
}
