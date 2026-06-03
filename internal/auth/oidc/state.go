package oidc

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/CherryHQ/stella/internal/auth"
)

const (
	stateCookieName   = "stella_oidc_state"
	stateCookieMaxAge = 600 // 10 minutes
	stateHMACPurpose  = "stella-oidc-state-v1"
)

// StateCookiePayload is the signed state stored in the OIDC state cookie.
// It ties the CSRF state string and PKCE code verifier together so they can be
// validated when the IdP redirects back to the callback endpoint.
type StateCookiePayload struct {
	State        string    `json:"state"`
	CodeVerifier string    `json:"code_verifier"`
	ProviderName string    `json:"provider_name"`
	CreatedAt    time.Time `json:"created_at"`
}

// StateManager creates and validates signed OIDC state cookies.
// The cookie payload is JSON-encoded then HMAC-SHA256 signed using a key
// derived from STELLA_VAULT_KEY. The signature is appended as a hex suffix
// after a "." separator: base64(payload).hex(sig).
type StateManager struct {
	secret []byte
}

// NewStateManager creates a StateManager. vaultKey is the raw STELLA_VAULT_KEY;
// a per-purpose key is derived via HKDF-SHA256.
func NewStateManager(vaultKey string) (*StateManager, error) {
	if vaultKey == "" {
		return nil, errors.New("oidc: STELLA_VAULT_KEY is required for state signing")
	}
	key, err := auth.DeriveHMACKey(vaultKey, stateHMACPurpose)
	if err != nil {
		return nil, fmt.Errorf("oidc: derive state key: %w", err)
	}
	return &StateManager{secret: key}, nil
}

// Generate creates a new StateCookiePayload with a random state and PKCE
// code verifier (S256 method).
func (m *StateManager) Generate() (StateCookiePayload, error) {
	state, err := randomHex(16)
	if err != nil {
		return StateCookiePayload{}, fmt.Errorf("oidc: generate state: %w", err)
	}
	verifier, err := randomBase64URL(32)
	if err != nil {
		return StateCookiePayload{}, fmt.Errorf("oidc: generate code verifier: %w", err)
	}
	return StateCookiePayload{
		State:        state,
		CodeVerifier: verifier,
		CreatedAt:    time.Now().UTC(),
	}, nil
}

// SetCookie encodes, signs, and writes the state cookie to the response.
func (m *StateManager) SetCookie(w http.ResponseWriter, payload StateCookiePayload, secure bool) error {
	value, err := m.encode(payload)
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     stateCookieName,
		Value:    value,
		Path:     "/",
		MaxAge:   stateCookieMaxAge,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
	return nil
}

// ValidateAndClear reads the state cookie, verifies the HMAC signature and
// expiry, checks that queryState matches the stored state string, clears the
// cookie, and returns the payload. Any mismatch returns an error.
func (m *StateManager) ValidateAndClear(w http.ResponseWriter, r *http.Request, queryState string) (StateCookiePayload, error) {
	c, err := r.Cookie(stateCookieName)
	if err != nil {
		return StateCookiePayload{}, errors.New("oidc: state cookie missing")
	}

	// Always clear the cookie regardless of validation outcome.
	http.SetCookie(w, &http.Cookie{
		Name:     stateCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})

	payload, err := m.decode(c.Value)
	if err != nil {
		return StateCookiePayload{}, fmt.Errorf("oidc: invalid state cookie: %w", err)
	}

	if time.Since(payload.CreatedAt) > stateCookieMaxAge*time.Second {
		return StateCookiePayload{}, errors.New("oidc: state cookie expired")
	}

	if payload.State != queryState {
		return StateCookiePayload{}, errors.New("oidc: state mismatch")
	}

	return payload, nil
}

// encode returns base64url(json(payload)) + "." + hex(hmac(base64url(json(payload)))).
func (m *StateManager) encode(payload StateCookiePayload) (string, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("oidc: marshal state: %w", err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(data)
	sig := auth.HMACSign(m.secret, []byte(encoded))
	return encoded + "." + hex.EncodeToString(sig), nil
}

// decode splits the cookie value, verifies the HMAC, and unmarshals the payload.
// Format: base64url(json(payload)) + "." + hex(hmac) — the dot is at position len-65.
func (m *StateManager) decode(value string) (StateCookiePayload, error) {
	// hex(sha256) = 64 chars; dot separator = 1 char → dot is at len-65.
	dot := len(value) - 65
	if dot <= 0 || value[dot] != '.' {
		return StateCookiePayload{}, errors.New("malformed")
	}
	encoded := value[:dot]
	sigHex := value[dot+1:]

	sig, err := hex.DecodeString(sigHex)
	if err != nil {
		return StateCookiePayload{}, errors.New("invalid signature encoding")
	}
	if !auth.HMACVerify(m.secret, []byte(encoded), sig) {
		return StateCookiePayload{}, errors.New("signature verification failed")
	}

	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return StateCookiePayload{}, fmt.Errorf("decode payload: %w", err)
	}
	var p StateCookiePayload
	if err := json.Unmarshal(data, &p); err != nil {
		return StateCookiePayload{}, fmt.Errorf("unmarshal payload: %w", err)
	}
	return p, nil
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func randomBase64URL(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
