package auth

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
	"sync"
	"time"
)

const (
	// linkCodeLength is the number of characters in a link code.
	linkCodeLength = 6

	// linkCodeTTL is how long a link code remains valid.
	linkCodeTTL = 5 * time.Minute
)

// linkCodeEntry holds a pending link code with its owner and expiry.
type linkCodeEntry struct {
	UserID   int64
	Platform string
	ExpireAt time.Time
}

// LinkCodeStore manages in-memory link codes for channel account linking.
// Codes are single-use and expire after 5 minutes. Not persisted to DB;
// restart clears all pending codes (acceptable for MVP).
type LinkCodeStore struct {
	codes sync.Map // string -> linkCodeEntry
}

// NewLinkCodeStore creates a new link code store.
func NewLinkCodeStore() *LinkCodeStore {
	return &LinkCodeStore{}
}

// Generate creates a new 6-character alphanumeric link code for the given
// user and platform. Returns the code string.
func (s *LinkCodeStore) Generate(userID int64, platform string) string {
	code := randomAlphanumeric(linkCodeLength)
	s.codes.Store(code, linkCodeEntry{
		UserID:   userID,
		Platform: platform,
		ExpireAt: time.Now().Add(linkCodeTTL),
	})
	return code
}

// Consume looks up a link code and returns the associated user ID and
// platform if valid. The code is consumed (deleted) on success.
// Returns (0, "", false) if the code is invalid or expired.
func (s *LinkCodeStore) Consume(code string) (int64, string, bool) {
	code = strings.ToUpper(strings.TrimSpace(code))
	val, ok := s.codes.LoadAndDelete(code)
	if !ok {
		return 0, "", false
	}
	entry := val.(linkCodeEntry)
	if time.Now().After(entry.ExpireAt) {
		return 0, "", false
	}
	return entry.UserID, entry.Platform, true
}

// IsLinkCode returns true if the string looks like a valid link code format
// (6 alphanumeric characters). This is a quick check before attempting Consume.
func IsLinkCode(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) != linkCodeLength {
		return false
	}
	for _, c := range s {
		if !isAlphanumeric(c) {
			return false
		}
	}
	return true
}

// randomAlphanumeric generates an uppercase alphanumeric string of length n.
func randomAlphanumeric(n int) string {
	// Generate more bytes than needed to account for hex encoding,
	// then filter to alphanumeric and take first n.
	b := make([]byte, n*2)
	if _, err := rand.Read(b); err != nil {
		panic("auth: crypto/rand failed: " + err.Error())
	}
	hex := strings.ToUpper(hex.EncodeToString(b))
	// Hex output is 0-9A-F, all alphanumeric. Take first n chars.
	return hex[:n]
}

func isAlphanumeric(c rune) bool {
	return (c >= '0' && c <= '9') || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}
