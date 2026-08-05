package credential

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"hash/crc32"
	"strings"
)

// Family prefixes. These are the DISPATCH namespace (which resolver handles a
// bearer), reserved up front so surfaces never collide. A public_id (below) is a
// separate concept: the indexed LOOKUP key inside a family.
const (
	// PATPrefix identifies a personal access token presented to the API.
	PATPrefix = "stella_pat_"
	// ProvisioningPrefix identifies a restricted provisioning token presented to
	// the API. Its distinct wire namespace must match the stored token use.
	ProvisioningPrefix = "stella_prv_"
	// OAuthAccessPrefix identifies an OAuth2 access token. Reserved for #613.
	OAuthAccessPrefix = "stella_oat_"
	// OAuthRefreshPrefix identifies an OAuth2 refresh token. Valid only at the
	// token endpoint -- hard-rejected at the API boundary. Reserved for #613.
	OAuthRefreshPrefix = "stella_ort_"
)

const (
	// publicIDBytes -> hex, so the public id never contains '_' and stays
	// splittable from the secret. It is a lookup key, not a secret.
	publicIDBytes = 12
	// secretBytes is the high-entropy secret: 32 bytes = 256-bit from a CSPRNG.
	secretBytes = 32
	// secretEncodedLen is the fixed base64url (no padding) length of secretBytes.
	secretEncodedLen = 43
	// crcLen is the fixed hex length of the trailing CRC32 checksum, so secret
	// scanners can validate a leaked token offline (GitHub-style).
	crcLen = 8
)

// Minted is the output of MintOpaque: the one-time plaintext plus the fields
// persisted for later O(1) lookup and verification.
type Minted struct {
	Plaintext string // shown to the user exactly once
	PublicID  string // indexed lookup key
	TokenHash string // SHA-256 hex of the secret; what we store and compare
	Last4     string // last 4 chars of the plaintext, for display
}

// MintOpaque mints a high-entropy opaque bearer token for a given kind. It is
// the kind-checked entry point for opaque PAT/OAuth-access tokens. It must not
// absorb client_secret password-hashing -- that is a distinct concern kept out
// of the generic mint. OAuth refresh tokens share the same wire format but
// rotate outside the API front door, so internal/oidc mints them via
// MintOpaqueWithPrefix rather than this kind-checked form.
func MintOpaque(kind Kind) (Minted, error) {
	prefix, err := opaquePrefix(kind)
	if err != nil {
		return Minted{}, err
	}
	return MintOpaqueWithPrefix(prefix)
}

// MintOpaqueWithPrefix mints an opaque token for an explicit prefix. It is the
// single definition of the opaque-token wire format -- prefix + public_id + "_" +
// secret + crc32, with the secret stored as a SHA-256 hash. MintOpaque is the
// PAT/OAuth-access entry point; internal/oidc uses this lower-level form for
// stella_ort_ refresh tokens so the format lives in exactly one place.
func MintOpaqueWithPrefix(prefix string) (Minted, error) {
	pubRaw := make([]byte, publicIDBytes)
	if _, err := rand.Read(pubRaw); err != nil {
		return Minted{}, fmt.Errorf("credential: generate public id: %w", err)
	}
	secRaw := make([]byte, secretBytes)
	if _, err := rand.Read(secRaw); err != nil {
		return Minted{}, fmt.Errorf("credential: generate secret: %w", err)
	}
	publicID := hex.EncodeToString(pubRaw)
	secret := base64.RawURLEncoding.EncodeToString(secRaw)
	body := prefix + publicID + "_" + secret
	plaintext := body + checksum(body)
	return Minted{
		Plaintext: plaintext,
		PublicID:  publicID,
		TokenHash: hashSecret(secret),
		Last4:     lastN(plaintext, 4),
	}, nil
}

func opaquePrefix(kind Kind) (string, error) {
	switch kind {
	case KindPAT:
		return PATPrefix, nil
	case KindProvisioning:
		return ProvisioningPrefix, nil
	case KindOAuth:
		return OAuthAccessPrefix, nil
	default:
		return "", fmt.Errorf("credential: MintOpaque does not mint kind %q", kind)
	}
}

// ParseOpaqueToken is the exported opaque-token splitter for internal/oidc's
// refresh tokens, which share the wire format but are resolved at /oauth/token
// rather than the API front door. Same contract as the internal resolver path.
func ParseOpaqueToken(prefix, raw string) (publicID, secret string, err error) {
	return parseOpaqueToken(prefix, raw)
}

// HashSecret returns the SHA-256 hex of an opaque-token secret. Exported so
// internal/oidc hashes refresh-token secrets against the same format authority
// instead of re-deriving it.
func HashSecret(secret string) string { return hashSecret(secret) }

// parseOpaqueToken splits a "<prefix><public_id>_<secret><crc>" token, verifying
// the trailing checksum. It returns the public id (lookup key) and the secret
// (hashed and compared against storage).
func parseOpaqueToken(prefix, raw string) (publicID, secret string, err error) {
	rest := strings.TrimPrefix(raw, prefix)
	publicID, tail, ok := strings.Cut(rest, "_")
	if !ok || publicID == "" {
		return "", "", fmt.Errorf("credential: malformed token")
	}
	if len(tail) != secretEncodedLen+crcLen {
		return "", "", fmt.Errorf("credential: malformed token")
	}
	secret = tail[:secretEncodedLen]
	crc := tail[secretEncodedLen:]
	body := prefix + publicID + "_" + secret
	if crc != checksum(body) {
		return "", "", fmt.Errorf("credential: token checksum mismatch")
	}
	return publicID, secret, nil
}

// hashSecret returns the SHA-256 hex of the high-entropy secret. SHA-256 is
// correct here: PATs/access tokens are random and high-entropy, so the slow
// password hashers (argon2/bcrypt) would only waste CPU per request.
func hashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
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
