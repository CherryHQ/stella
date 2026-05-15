package auth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log/slog"
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
	UserID   string
	Platform string
	ExpireAt time.Time
}

// LinkCodeStore manages single-use link codes for channel account linking.
// Codes expire after 5 minutes. The default constructor keeps them in-memory;
// the shared constructor persists them to the Stella DB for cross-process use.
type LinkCodeStore struct {
	codes sync.Map // string -> linkCodeEntry
	db    *sql.DB
}

// NewLinkCodeStore creates a new link code store.
func NewLinkCodeStore() *LinkCodeStore {
	return &LinkCodeStore{}
}

// NewSharedLinkCodeStore creates a link code store backed by the shared Stella DB
// so admin and channel subprocesses can exchange codes across processes.
func NewSharedLinkCodeStore(ctx context.Context, db *sql.DB) (*LinkCodeStore, error) {
	if db == nil {
		return nil, fmt.Errorf("link code store: nil db")
	}

	store := &LinkCodeStore{db: db}
	if err := store.ensureSchema(ctx); err != nil {
		return nil, err
	}
	return store, nil
}

// Generate creates a new 6-character alphanumeric link code for the given
// user and platform. Returns the code string.
func (s *LinkCodeStore) Generate(userID string, platform string) string {
	code := randomAlphanumeric(linkCodeLength)
	if s.db != nil {
		if err := s.generateShared(context.Background(), code, userID, platform); err != nil {
			slog.Error("link code: persist generate failed", "platform", platform, "user_id", userID, "error", err)
			return code
		}
		return code
	}

	s.codes.Store(code, linkCodeEntry{
		UserID:   userID,
		Platform: platform,
		ExpireAt: time.Now().Add(linkCodeTTL),
	})
	return code
}

// Consume looks up a link code and returns the associated user ID and
// platform if valid. The code is consumed (deleted) on success.
// Returns ("", "", false) if the code is invalid or expired.
func (s *LinkCodeStore) Consume(code string) (string, string, bool) {
	code = strings.ToUpper(strings.TrimSpace(code))
	if s.db != nil {
		return s.consumeShared(context.Background(), code)
	}

	val, ok := s.codes.LoadAndDelete(code)
	if !ok {
		return "", "", false
	}
	entry := val.(linkCodeEntry)
	if time.Now().After(entry.ExpireAt) {
		return "", "", false
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

func (s *LinkCodeStore) ensureSchema(ctx context.Context) error {
	// Drop and recreate to ensure the schema is current. Link codes are
	// ephemeral (5-minute TTL), so losing in-flight codes on restart is safe.
	if _, err := s.db.ExecContext(ctx, `DROP TABLE IF EXISTS auth_link_codes`); err != nil {
		return fmt.Errorf("link code store: drop schema: %w", err)
	}
	const stmt = `CREATE TABLE auth_link_codes (
		code TEXT PRIMARY KEY,
		user_id TEXT NOT NULL,
		platform TEXT NOT NULL,
		expire_at INTEGER NOT NULL
	)`
	if _, err := s.db.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("link code store: create schema: %w", err)
	}
	return nil
}

func (s *LinkCodeStore) generateShared(ctx context.Context, code string, userID string, platform string) error {
	if err := s.deleteExpired(ctx); err != nil {
		return err
	}

	const stmt = `INSERT OR REPLACE INTO auth_link_codes (code, user_id, platform, expire_at)
	VALUES (?, ?, ?, ?)`
	_, err := s.db.ExecContext(ctx, stmt, code, userID, platform, time.Now().Add(linkCodeTTL).Unix())
	if err != nil {
		return fmt.Errorf("link code store: insert code: %w", err)
	}
	return nil
}

func (s *LinkCodeStore) consumeShared(ctx context.Context, code string) (string, string, bool) {
	if err := s.deleteExpired(ctx); err != nil {
		slog.Error("link code: purge expired failed", "error", err)
		return "", "", false
	}

	const stmt = `DELETE FROM auth_link_codes
	WHERE code = ?
	RETURNING user_id, platform, expire_at`

	var (
		userID   string
		platform string
		expireAt int64
	)
	err := s.db.QueryRowContext(ctx, stmt, code).Scan(&userID, &platform, &expireAt)
	if err == sql.ErrNoRows {
		return "", "", false
	}
	if err != nil {
		slog.Error("link code: consume failed", "code", code, "error", err)
		return "", "", false
	}
	if time.Now().After(time.Unix(expireAt, 0)) {
		return "", "", false
	}
	return userID, platform, true
}

func (s *LinkCodeStore) deleteExpired(ctx context.Context) error {
	const stmt = `DELETE FROM auth_link_codes WHERE expire_at <= ?`
	_, err := s.db.ExecContext(ctx, stmt, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("link code store: delete expired: %w", err)
	}
	return nil
}
