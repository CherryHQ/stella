package auth

// TokenService is kept as the server's bearer-auth wiring handle. Scoped sandbox
// tokens were retired; PAT/OAuth minting and verification live in internal/credential.
type TokenService struct{}

// NewTokenService creates a token service handle backed by credential storage.
func NewTokenService(_ any) *TokenService {
	return &TokenService{}
}
