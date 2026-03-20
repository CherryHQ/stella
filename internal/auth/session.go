package auth

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"time"
)

const (
	// SessionCookieName is the HTTP cookie name for session tokens.
	SessionCookieName = "anna_session"

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
func ClearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
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
