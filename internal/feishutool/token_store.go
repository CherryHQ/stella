package feishutool

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"fmt"
	"io"
	"time"

	"golang.org/x/crypto/hkdf"

	"github.com/vaayne/anna/internal/db/sqlc"
)

const (
	// hkdfSalt is the constant salt for HKDF key derivation.
	hkdfSalt = "feishu-token-encryption"
	// hkdfInfo is the info string for HKDF key derivation.
	hkdfInfo = "feishu-uat-v1"
	// timeLayout is the ISO 8601 layout used for token expiry times.
	timeLayout = time.RFC3339
)

// Token holds a Feishu user access token pair with expiry information.
type Token struct {
	AccessToken      string
	RefreshToken     string
	ExpiresAt        time.Time
	RefreshExpiresAt time.Time
}

// IsExpired returns true if the access token has expired.
func (t Token) IsExpired() bool {
	return time.Now().After(t.ExpiresAt)
}

// IsRefreshExpired returns true if the refresh token has expired.
func (t Token) IsRefreshExpired() bool {
	return time.Now().After(t.RefreshExpiresAt)
}

// TokenStore manages Feishu user access tokens.
type TokenStore interface {
	Get(ctx context.Context, openID string) (Token, error)
	Set(ctx context.Context, openID string, token Token) error
	Delete(ctx context.Context, openID string) error
}

// SQLiteTokenStore implements TokenStore using SQLite with AES-256-GCM encryption.
type SQLiteTokenStore struct {
	queries *sqlc.Queries
	gcm     cipher.AEAD
}

// NewSQLiteTokenStore creates a new SQLite-backed token store.
// The encryption key is derived from appSecret using HKDF with SHA-256.
func NewSQLiteTokenStore(db *sql.DB, appSecret string) (*SQLiteTokenStore, error) {
	key, err := deriveKey(appSecret)
	if err != nil {
		return nil, fmt.Errorf("feishutool: derive encryption key: %w", err)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("feishutool: create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("feishutool: create GCM: %w", err)
	}

	return &SQLiteTokenStore{
		queries: sqlc.New(db),
		gcm:     gcm,
	}, nil
}

// Get retrieves and decrypts a token for the given open_id.
// Returns sql.ErrNoRows if no token is stored for this user.
func (s *SQLiteTokenStore) Get(ctx context.Context, openID string) (Token, error) {
	row, err := s.queries.GetFeishuToken(ctx, openID)
	if err != nil {
		return Token{}, err
	}

	accessToken, err := s.decrypt(row.AccessToken)
	if err != nil {
		return Token{}, fmt.Errorf("feishutool: decrypt access token: %w", err)
	}

	refreshToken, err := s.decrypt(row.RefreshToken)
	if err != nil {
		return Token{}, fmt.Errorf("feishutool: decrypt refresh token: %w", err)
	}

	expiresAt, err := time.Parse(timeLayout, row.ExpiresAt)
	if err != nil {
		return Token{}, fmt.Errorf("feishutool: parse expires_at: %w", err)
	}

	refreshExpiresAt, err := time.Parse(timeLayout, row.RefreshExpiresAt)
	if err != nil {
		return Token{}, fmt.Errorf("feishutool: parse refresh_expires_at: %w", err)
	}

	return Token{
		AccessToken:      accessToken,
		RefreshToken:     refreshToken,
		ExpiresAt:        expiresAt,
		RefreshExpiresAt: refreshExpiresAt,
	}, nil
}

// Set encrypts and stores a token for the given open_id.
// Upserts if a token already exists for this user.
func (s *SQLiteTokenStore) Set(ctx context.Context, openID string, token Token) error {
	encAccessToken, err := s.encrypt(token.AccessToken)
	if err != nil {
		return fmt.Errorf("feishutool: encrypt access token: %w", err)
	}

	encRefreshToken, err := s.encrypt(token.RefreshToken)
	if err != nil {
		return fmt.Errorf("feishutool: encrypt refresh token: %w", err)
	}

	return s.queries.UpsertFeishuToken(ctx, sqlc.UpsertFeishuTokenParams{
		OpenID:           openID,
		AccessToken:      encAccessToken,
		RefreshToken:     encRefreshToken,
		ExpiresAt:        token.ExpiresAt.Format(timeLayout),
		RefreshExpiresAt: token.RefreshExpiresAt.Format(timeLayout),
	})
}

// Delete removes a stored token for the given open_id.
func (s *SQLiteTokenStore) Delete(ctx context.Context, openID string) error {
	return s.queries.DeleteFeishuToken(ctx, openID)
}

// encrypt encrypts plaintext using AES-256-GCM and returns base64-encoded ciphertext.
// The nonce is prepended to the ciphertext before encoding.
func (s *SQLiteTokenStore) encrypt(plaintext string) (string, error) {
	nonce := make([]byte, s.gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}

	ciphertext := s.gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// decrypt decodes base64 ciphertext and decrypts it using AES-256-GCM.
// Expects nonce to be prepended to the ciphertext.
func (s *SQLiteTokenStore) decrypt(encoded string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("base64 decode: %w", err)
	}

	nonceSize := s.gcm.NonceSize()
	if len(data) < nonceSize {
		return "", fmt.Errorf("ciphertext too short")
	}

	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	plaintext, err := s.gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt: %w", err)
	}

	return string(plaintext), nil
}

// deriveKey derives a 32-byte AES key from appSecret using HKDF with SHA-256.
func deriveKey(appSecret string) ([]byte, error) {
	reader := hkdf.New(sha256.New, []byte(appSecret), []byte(hkdfSalt), []byte(hkdfInfo))
	key := make([]byte, 32) // AES-256
	if _, err := io.ReadFull(reader, key); err != nil {
		return nil, err
	}
	return key, nil
}
