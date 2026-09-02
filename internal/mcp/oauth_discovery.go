package mcp

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"
)

// oauth2Context returns a context whose HTTP client is the SSRF-safe one, so
// x/oauth2 exchange and refresh calls obey the same dial policy as discovery.
func oauth2Context(policy EndpointPolicy) context.Context {
	return context.WithValue(context.Background(), oauth2.HTTPClient, oauthHTTPClient(policy))
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

// resolveOAuthClient returns the client credentials for one registration:
// the pre-registered client from metadata + vault when configured, otherwise
// a DCR registration whose result is persisted so it runs once per
// registration.
func (s *Service) resolveOAuthClient(ctx context.Context, reg Registration, asm *oauthex.AuthServerMeta, callback string) (clientID, clientSecret string, err error) {
	if reg.OAuthClientID != "" {
		secret := ""
		if s.vault != nil {
			secret, _ = s.vault.GetScoped(ctx, reg.Scope, reg.UserID, reg.AgentID, oauthClientSecretName(reg.ID))
		}
		return reg.OAuthClientID, secret, nil
	}
	if asm.RegistrationEndpoint == "" {
		return "", "", fmt.Errorf("mcp: server has no registration endpoint and no pre-registered client is configured")
	}
	resp, err := oauthex.RegisterClient(ctx, asm.RegistrationEndpoint, &oauthex.ClientRegistrationMetadata{
		RedirectURIs:            []string{callback},
		TokenEndpointAuthMethod: "none",
		GrantTypes:              []string{"authorization_code", "refresh_token"},
		ResponseTypes:           []string{"code"},
		ClientName:              "Stella",
	}, oauthHTTPClient(s.endpoints))
	if err != nil {
		return "", "", fmt.Errorf("mcp: dynamic client registration: %w", err)
	}
	if err := s.persistDCRClient(ctx, reg, resp); err != nil {
		return "", "", err
	}
	return resp.ClientID, resp.ClientSecret, nil
}

// persistDCRClient writes the issued client id into metadata.oauth.client_id
// (public) and the secret, when the server issued one, into the vault.
func (s *Service) persistDCRClient(ctx context.Context, reg Registration, resp *oauthex.ClientRegistrationResponse) error {
	metadata, err := oauthClientMetadata([]byte(`{}`), resp.ClientID)
	if err != nil {
		return err
	}
	if err := s.db.UpdateMCPServerMetadata(ctx, UpdateMCPServerMetadataParams{ID: reg.ID, Metadata: metadata}); err != nil {
		return fmt.Errorf("mcp: persist oauth client id: %w", err)
	}
	if resp.ClientSecret != "" && s.vault != nil {
		if err := s.storeToken(ctx, reg.Scope, reg.UserID, reg.AgentID, oauthClientSecretName(reg.ID), resp.ClientSecret); err != nil {
			return fmt.Errorf("mcp: store oauth client secret: %w", err)
		}
	}
	return nil
}
