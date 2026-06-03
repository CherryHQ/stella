package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"golang.org/x/crypto/hkdf"
)

const (
	// SessionCookieName is the HTTP cookie name for session tokens.
	SessionCookieName = "stella_session"

	// SessionDuration is the default session lifetime.
	SessionDuration = 7 * 24 * time.Hour

	// sessionIDBytes is the number of random bytes for a session ID.
	sessionIDBytes = 32
)

// ErrNoSession is returned when no session cookie is present.
var ErrNoSession = errors.New("no session cookie")

// NewSessionID generates a cryptographically random hex-encoded session ID
// (32 bytes = 64 hex characters).
func NewSessionID() string {
	b := make([]byte, sessionIDBytes)
	if _, err := rand.Read(b); err != nil {
		panic("auth: crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// SetSessionCookie writes the session cookie to the response. The Secure flag
// is set when secure is true (i.e., not localhost/dev).
func SetSessionCookie(w http.ResponseWriter, sessionID string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    sessionID,
		Path:     "/",
		MaxAge:   int(SessionDuration.Seconds()),
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// ClearSessionCookie removes the session cookie from the response.
// Setting Secure=true is harmless on HTTP and ensures the cookie is properly
// cleared on HTTPS deployments.
func ClearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
}

// GetSessionCookie extracts the session ID from the request cookie.
// Returns ErrNoSession if the cookie is missing or empty.
func GetSessionCookie(r *http.Request) (string, error) {
	c, err := r.Cookie(SessionCookieName)
	if err != nil {
		return "", ErrNoSession
	}
	if c.Value == "" {
		return "", ErrNoSession
	}
	return c.Value, nil
}

// --- SessionManager (token-hash sessions) ---

// SessionManager creates and validates login sessions backed by SessionStore.
// Raw tokens are stored in cookies; only SHA-256 hashes are stored in the DB.
type SessionManager struct {
	store  SessionStore
	secret []byte // HKDF-derived from STELLA_VAULT_KEY, used for future MAC needs
}

// NewSessionManager creates a SessionManager. vaultKey is the raw STELLA_VAULT_KEY
// value; a per-purpose key is derived via HKDF-SHA256.
func NewSessionManager(store SessionStore, vaultKey string) (*SessionManager, error) {
	if vaultKey == "" {
		return nil, errors.New("auth: STELLA_VAULT_KEY is required for session management")
	}
	secret, err := deriveKey([]byte(vaultKey), "stella-session-v1", 32)
	if err != nil {
		return nil, fmt.Errorf("auth: derive session key: %w", err)
	}
	return &SessionManager{store: store, secret: secret}, nil
}

// WithStore returns a shallow copy of the SessionManager backed by a different
// SessionStore. Used to run session creation inside a DB transaction.
func (m *SessionManager) WithStore(store SessionStore) *SessionManager {
	return &SessionManager{store: store, secret: m.secret}
}

// Create generates a new session for userID. Returns the raw token to set as
// a cookie value; only the SHA-256 hash is stored in the DB.
func (m *SessionManager) Create(ctx context.Context, userID string) (rawToken string, session Session, err error) {
	raw, err := generateRawToken()
	if err != nil {
		return "", Session{}, err
	}
	s := Session{
		ID:        NewSessionID(),
		UserID:    userID,
		TokenHash: hashSessionToken(raw),
		ExpiresAt: time.Now().UTC().Add(SessionDuration),
	}
	created, err := m.store.CreateSession(ctx, s)
	if err != nil {
		return "", Session{}, fmt.Errorf("auth: create session: %w", err)
	}
	return raw, created, nil
}

// Validate looks up the session by the SHA-256 hash of rawToken and returns it.
// Returns an error if the session is not found or has expired.
func (m *SessionManager) Validate(ctx context.Context, rawToken string) (Session, error) {
	hash := hashSessionToken(rawToken)
	s, err := m.store.GetSessionByTokenHash(ctx, hash)
	if err != nil {
		return Session{}, fmt.Errorf("auth: session not found: %w", err)
	}
	if time.Now().After(s.ExpiresAt) {
		_ = m.store.DeleteSession(ctx, s.ID)
		return Session{}, errors.New("auth: session expired")
	}
	return s, nil
}

// Extend updates the session expiry to now + SessionDuration.
func (m *SessionManager) Extend(ctx context.Context, sessionID string) error {
	return m.store.UpdateSessionExpiry(ctx, sessionID, time.Now().UTC().Add(SessionDuration))
}

// Revoke deletes the session from the DB.
func (m *SessionManager) Revoke(ctx context.Context, sessionID string) error {
	return m.store.DeleteSession(ctx, sessionID)
}

// SetCookie writes the session token cookie to the response.
func (m *SessionManager) SetCookie(w http.ResponseWriter, rawToken string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    rawToken,
		Path:     "/",
		MaxAge:   int(SessionDuration.Seconds()),
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// ClearCookie removes the session cookie from the response.
func (m *SessionManager) ClearCookie(w http.ResponseWriter) {
	ClearSessionCookie(w)
}

// GetToken extracts the raw token from the request cookie.
func (m *SessionManager) GetToken(r *http.Request) (string, error) {
	return GetSessionCookie(r)
}

// hashSessionToken returns the hex-encoded SHA-256 hash of rawToken.
func hashSessionToken(rawToken string) string {
	sum := sha256.Sum256([]byte(rawToken))
	return hex.EncodeToString(sum[:])
}

// generateRawToken produces a 32-byte cryptographically random hex token.
func generateRawToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("auth: generate session token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// deriveKey derives a fixed-length key from masterKey using HKDF-SHA256 with
// the given info string as the purpose label.
func deriveKey(masterKey []byte, info string, length int) ([]byte, error) {
	h := hkdf.New(sha256.New, masterKey, nil, []byte(info))
	key := make([]byte, length)
	if _, err := io.ReadFull(h, key); err != nil {
		return nil, err
	}
	return key, nil
}

// DeriveHMACKey derives a per-purpose HMAC key from the vault key.
// Used by StateManager in the oidc package.
func DeriveHMACKey(vaultKey, purpose string) ([]byte, error) {
	return deriveKey([]byte(vaultKey), purpose, 32)
}

// HMACSign returns an HMAC-SHA256 signature of msg using key.
func HMACSign(key, msg []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(msg)
	return mac.Sum(nil)
}

// HMACVerify returns true if sig is a valid HMAC-SHA256 of msg under key.
func HMACVerify(key, msg, sig []byte) bool {
	expected := HMACSign(key, msg)
	return hmac.Equal(expected, sig)
}
