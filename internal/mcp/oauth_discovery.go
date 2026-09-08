package mcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"

	"github.com/CherryHQ/stella/internal/authz"
)

type oauthAuthorityContextKey struct{}

// ErrOAuthClientInitializationRequired tells a non-admin caller that a
// system-scoped config needs one administrator-owned DCR initialization before
// users may authorize their per-user account.
var ErrOAuthClientInitializationRequired = errors.New("mcp: OAuth client initialization requires an administrator")

const (
	oauthTokenEndpointAuthMethodBasic = "client_secret_basic"
	oauthTokenEndpointAuthMethodPost  = "client_secret_post"
	oauthTokenEndpointAuthMethodNone  = "none"
)

// oauthTokenEndpointAuthStyle validates the RFC 7591 token endpoint auth
// method and returns both the x/oauth2 style and its canonical wire value.
// An omitted method has the RFC default, client_secret_basic.
func oauthTokenEndpointAuthStyle(method string) (oauth2.AuthStyle, string, error) {
	if method == "" {
		method = oauthTokenEndpointAuthMethodBasic
	}
	switch method {
	case oauthTokenEndpointAuthMethodBasic:
		return oauth2.AuthStyleInHeader, method, nil
	case oauthTokenEndpointAuthMethodPost:
		return oauth2.AuthStyleInParams, method, nil
	case oauthTokenEndpointAuthMethodNone:
		return oauth2.AuthStyleInParams, method, nil
	default:
		return 0, "", fmt.Errorf("mcp: unsupported OAuth token endpoint auth method %q", method)
	}
}

func withOAuthAuthority(ctx context.Context, authority authz.Authority) context.Context {
	return context.WithValue(ctx, oauthAuthorityContextKey{}, authority)
}

func oauthAuthority(ctx context.Context) (authz.Authority, bool) {
	authority, ok := ctx.Value(oauthAuthorityContextKey{}).(authz.Authority)
	return authority, ok && authority.Valid()
}

// oauth2Context returns a context whose HTTP client is the SSRF-safe one, so
// x/oauth2 exchange and refresh calls obey the same dial policy as discovery.
func oauth2Context(parent context.Context, policy EndpointPolicy) context.Context {
	return context.WithValue(parent, oauth2.HTTPClient, oauthHTTPClient(policy))
}

// challenge fetch + PRM/AS discovery + client resolution for StartOAuth.
// Every outbound request rides oauthHTTPClient(): the SSRF-safe dialer and
// redirect policy apply to metadata, registration, and token endpoints exactly
// as they do to MCP traffic (#1196).

// fetchChallenge asks the MCP endpoint for a 401 WWW-Authenticate challenge.
// A 2xx answer means the server needs no authorization at all.
func fetchChallenge(ctx context.Context, reg Registration, policy EndpointPolicy) ([]oauthex.Challenge, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reg.URL, nil)
	if err != nil {
		return nil, fmt.Errorf("mcp: invalid endpoint url: malformed URL")
	}
	resp, err := oauthHTTPClient(policy).Do(req)
	if err != nil {
		return nil, fmt.Errorf("mcp: reach endpoint: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil, fmt.Errorf("mcp: endpoint does not require authorization")
	}
	if resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusForbidden {
		return nil, fmt.Errorf("mcp: endpoint returned status %d", resp.StatusCode)
	}
	challenges, err := oauthex.ParseWWWAuthenticate(resp.Header[http.CanonicalHeaderKey("WWW-Authenticate")])
	if err != nil {
		return nil, fmt.Errorf("mcp: parse WWW-Authenticate: %w", err)
	}
	return challenges, nil
}

// protectedResourceMetadata resolves the PRM: from the challenge's
// resource_metadata URL when present, otherwise the well-known locations under
// the MCP endpoint, and finally the 2025-03-26 fallback that treats the MCP
// server root as the authorization server itself.
func protectedResourceMetadata(ctx context.Context, reg Registration, challenges []oauthex.Challenge, policy EndpointPolicy) (*oauthex.ProtectedResourceMetadata, error) {
	client := oauthHTTPClient(policy)
	for _, candidate := range protectedResourceMetadataURLs(challenges, reg.URL) {
		prm, err := oauthex.GetProtectedResourceMetadata(ctx, candidate.metadataURL, candidate.resource, client)
		if err != nil || prm == nil {
			continue
		}
		if len(prm.AuthorizationServers) == 0 {
			return nil, fmt.Errorf("mcp: protected resource metadata has no authorization servers")
		}
		return prm, nil
	}
	// 2025-03-26 fallback: the MCP server root is the authorization server.
	root, err := url.Parse(reg.URL)
	if err != nil {
		return nil, fmt.Errorf("mcp: parse endpoint url: %w", err)
	}
	root.Path = ""
	return &oauthex.ProtectedResourceMetadata{
		Resource:             reg.URL,
		AuthorizationServers: []string{root.String()},
	}, nil
}

type prmCandidate struct{ metadataURL, resource string }

func protectedResourceMetadataURLs(challenges []oauthex.Challenge, serverURL string) []prmCandidate {
	var out []prmCandidate
	for _, c := range challenges {
		if u := c.Params["resource_metadata"]; u != "" {
			out = append(out, prmCandidate{metadataURL: u, resource: serverURL})
		}
	}
	if u, err := url.Parse(serverURL); err == nil {
		wellKnown := *u
		wellKnown.Path = strings.TrimSuffix(u.Path, "/") + "/.well-known/oauth-protected-resource"
		out = append(out, prmCandidate{metadataURL: wellKnown.String(), resource: serverURL})
		root := *u
		root.Path = ""
		out = append(out, prmCandidate{metadataURL: root.String() + "/.well-known/oauth-protected-resource", resource: serverURL})
	}
	return out
}

// authServerMetadata resolves the AS metadata, falling back to the 2025-03-26
// convention (/authorize /token /register under the issuer) when the issuer
// publishes none.
func authServerMetadata(ctx context.Context, issuer string, policy EndpointPolicy) (*oauthex.AuthServerMeta, error) {
	asm, err := auth.GetAuthServerMetadata(ctx, issuer, oauthHTTPClient(policy))
	if err != nil {
		return nil, fmt.Errorf("mcp: fetch authorization server metadata: %w", err)
	}
	if asm == nil {
		asm = &oauthex.AuthServerMeta{
			Issuer:                issuer,
			AuthorizationEndpoint: strings.TrimRight(issuer, "/") + "/authorize",
			TokenEndpoint:         strings.TrimRight(issuer, "/") + "/token",
			RegistrationEndpoint:  strings.TrimRight(issuer, "/") + "/register",
		}
	}
	if asm.AuthorizationEndpoint == "" || asm.TokenEndpoint == "" {
		return nil, fmt.Errorf("mcp: authorization server metadata is incomplete")
	}
	return asm, nil
}

// resolveOAuthClient resolves credentials only for filesystem registrations.
func (s *Service) resolveOAuthClient(ctx context.Context, reg Registration, asm *oauthex.AuthServerMeta, callback string) (Registration, string, string, oauth2.AuthStyle, error) {
	if !reg.IsFile() {
		return Registration{}, "", "", 0, ErrOAuthClientInitializationRequired
	}
	return s.resolveFileOAuthClient(ctx, reg, asm, callback)
}

func (s *Service) resolveFileOAuthClient(ctx context.Context, reg Registration, asm *oauthex.AuthServerMeta, callback string) (Registration, string, string, oauth2.AuthStyle, error) {
	if reg.OAuthClientID != "" {
		secret, err := s.oauthClientSecret(ctx, reg)
		if err != nil {
			return Registration{}, "", "", 0, err
		}
		method, _, err := oauthTokenEndpointAuthStyle(reg.TokenEndpointAuthMethod)
		if err != nil {
			return Registration{}, "", "", 0, err
		}
		return reg, reg.OAuthClientID, secret, method, nil
	}
	state, err := s.loadFileClientState(ctx, reg)
	if err != nil {
		return Registration{}, "", "", 0, err
	}
	if state.ClientID != "" {
		return reg, state.ClientID, state.ClientSecret, oauth2.AuthStyle(state.AuthStyle), nil
	}
	if asm.RegistrationEndpoint == "" {
		return Registration{}, "", "", 0, fmt.Errorf("mcp: server has no registration endpoint and no pre-registered client is configured")
	}
	authority, ok := oauthAuthority(ctx)
	if !ok || !authority.IsAdmin() && IsSystemScope(reg.Scope) {
		return Registration{}, "", "", 0, ErrOAuthClientInitializationRequired
	}
	resp, err := oauthex.RegisterClient(ctx, asm.RegistrationEndpoint, &oauthex.ClientRegistrationMetadata{
		RedirectURIs: []string{callback}, GrantTypes: []string{"authorization_code", "refresh_token"},
		ResponseTypes: []string{"code"}, ClientName: "Stella",
	}, oauthHTTPClient(s.endpoints))
	if err != nil {
		// Registration responses are remote-controlled and may echo client
		// secrets or other sensitive diagnostics. Keep this user-facing error
		// fixed; the underlying cause remains available only to local tracing.
		return Registration{}, "", "", 0, fmt.Errorf("mcp: file OAuth client registration failed")
	}
	style, _, err := oauthTokenEndpointAuthStyle(resp.TokenEndpointAuthMethod)
	if err != nil {
		return Registration{}, "", "", 0, err
	}
	winning, err := s.storeFileClientStateIfEmpty(ctx, reg, fileOAuthClientState{ClientID: resp.ClientID, ClientSecret: resp.ClientSecret, AuthStyle: int(style)})
	if err != nil {
		return Registration{}, "", "", 0, err
	}
	return reg, winning.ClientID, winning.ClientSecret, oauth2.AuthStyle(winning.AuthStyle), nil
}
