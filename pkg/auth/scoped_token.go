package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	ScopedTokenPrefix = "stella_scoped_"
	scopedTokenTTL    = 6 * time.Hour
)

var DefaultSandboxScopes = []string{
	"agent:read",
	"goals:*",
	"workflows:*",
	"scheduler:*",
	"skills:*",
	"shares:*",
	"recally:*",
	"email:*",
	"oauth:*",
	"vault:*",
}

type ScopedTokenClaims struct {
	Subject   string   `json:"sub"`
	UserID    string   `json:"user_id"`
	AgentID   string   `json:"agent_id"`
	SessionID string   `json:"session_id,omitempty"`
	ProjectID string   `json:"project_id,omitempty"`
	Scopes    []string `json:"scopes,omitempty"`
	ExpiresAt int64    `json:"exp"`
	IssuedAt  int64    `json:"iat"`
	TokenID   string   `json:"jti"`
}

func (c ScopedTokenClaims) Expired(now time.Time) bool {
	return c.ExpiresAt <= now.UTC().Unix()
}

func (c ScopedTokenClaims) HasScope(required string) bool {
	for _, scope := range c.Scopes {
		if scope == required {
			return true
		}
		prefix, _, ok := strings.Cut(scope, ":")
		if ok && scope == prefix+":*" && strings.HasPrefix(required, prefix+":") {
			return true
		}
	}
	return false
}

func SignScopedToken(secret []byte, claims ScopedTokenClaims, now time.Time) (string, error) {
	if len(secret) == 0 {
		return "", fmt.Errorf("scoped token secret is required")
	}
	if claims.UserID == "" {
		return "", fmt.Errorf("scoped token user_id is required")
	}
	if claims.AgentID == "" {
		return "", fmt.Errorf("scoped token agent_id is required")
	}
	if claims.Subject == "" {
		claims.Subject = claims.UserID
	}
	if len(claims.Scopes) == 0 {
		claims.Scopes = append([]string(nil), DefaultSandboxScopes...)
	}
	if claims.IssuedAt == 0 {
		claims.IssuedAt = now.UTC().Unix()
	}
	if claims.ExpiresAt == 0 {
		claims.ExpiresAt = now.UTC().Add(scopedTokenTTL).Unix()
	}
	if claims.TokenID == "" {
		id, err := randomTokenID()
		if err != nil {
			return "", err
		}
		claims.TokenID = id
	}

	header := map[string]string{"alg": "HS256", "typ": "JWT"}
	headerJSON, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signingInput := b64(headerJSON) + "." + b64(claimsJSON)
	return ScopedTokenPrefix + signingInput + "." + b64(scopedSignature(secret, signingInput)), nil
}

func VerifyScopedToken(secret []byte, rawToken string, now time.Time) (ScopedTokenClaims, error) {
	claims, signingInput, sig, err := parseScopedToken(rawToken)
	if err != nil {
		return ScopedTokenClaims{}, err
	}
	if !hmac.Equal(sig, scopedSignature(secret, signingInput)) {
		return ScopedTokenClaims{}, fmt.Errorf("invalid scoped token signature")
	}
	if claims.Expired(now) {
		return ScopedTokenClaims{}, fmt.Errorf("scoped token expired")
	}
	if claims.UserID == "" || claims.AgentID == "" {
		return ScopedTokenClaims{}, fmt.Errorf("scoped token missing required claims")
	}
	return claims, nil
}

func ParseScopedTokenUnverified(rawToken string) (ScopedTokenClaims, error) {
	claims, _, _, err := parseScopedToken(rawToken)
	return claims, err
}

func IsScopedToken(rawToken string) bool {
	return strings.HasPrefix(strings.TrimSpace(rawToken), ScopedTokenPrefix)
}

func parseScopedToken(rawToken string) (ScopedTokenClaims, string, []byte, error) {
	rawToken = strings.TrimSpace(rawToken)
	if !strings.HasPrefix(rawToken, ScopedTokenPrefix) {
		return ScopedTokenClaims{}, "", nil, fmt.Errorf("not a scoped token")
	}
	compact := strings.TrimPrefix(rawToken, ScopedTokenPrefix)
	parts := strings.Split(compact, ".")
	if len(parts) != 3 {
		return ScopedTokenClaims{}, "", nil, fmt.Errorf("malformed scoped token")
	}
	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return ScopedTokenClaims{}, "", nil, fmt.Errorf("decode scoped token header: %w", err)
	}
	var header struct {
		Alg string `json:"alg"`
		Typ string `json:"typ"`
	}
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		return ScopedTokenClaims{}, "", nil, fmt.Errorf("parse scoped token header: %w", err)
	}
	if header.Alg != "HS256" || header.Typ != "JWT" {
		return ScopedTokenClaims{}, "", nil, fmt.Errorf("unsupported scoped token header")
	}
	claimsJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ScopedTokenClaims{}, "", nil, fmt.Errorf("decode scoped token claims: %w", err)
	}
	var claims ScopedTokenClaims
	if err := json.Unmarshal(claimsJSON, &claims); err != nil {
		return ScopedTokenClaims{}, "", nil, fmt.Errorf("parse scoped token claims: %w", err)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return ScopedTokenClaims{}, "", nil, fmt.Errorf("decode scoped token signature: %w", err)
	}
	return claims, parts[0] + "." + parts[1], sig, nil
}

func scopedSignature(secret []byte, signingInput string) []byte {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(signingInput))
	return mac.Sum(nil)
}

func randomTokenID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate scoped token id: %w", err)
	}
	return b64(b), nil
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
