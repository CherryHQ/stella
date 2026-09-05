package mcp

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"
)

// oauthSession implements mcpauth.OAuthHandler for one (registration, owner)
// pair. It is never interactive: a 401/403 challenge is surfaced as a durable
// needs_auth status and an error the model can act on, while valid bundles are
// injected per request and refreshed singleflight-style just before expiry.
type oauthSession struct {
	svc   *Service
	reg   Registration
	owner CredentialOwner
}

// TokenSource is called per outgoing request by the streamable transport. The
// returned source loads the bundle fresh each call and refreshes through the
// singleflight lock, so a restart or a reconnect in another process is picked
// up immediately.
func (h *oauthSession) TokenSource(ctx context.Context) (oauth2.TokenSource, error) {
	if h.svc.oauthTokens == nil {
		return nil, fmt.Errorf("mcp: oauth requires the vault, which is not configured")
	}
	bundle, err := h.svc.oauthTokens.Load(ctx, oauthBundleRef(h.reg, h.owner))
	if err != nil {
		return nil, err
	}
	source := h.svc.oauthTokens.TokenSource(ctx, h.svc.oauthProviderConfig(ctx, h.reg, bundle), oauthBundleRef(h.reg, h.owner), oauthMinValidity)
	return tokenSourceFunc(func() (*oauth2.Token, error) {
		token, err := source.Token()
		if err == nil {
			return token, nil
		}
		_ = h.svc.SetStatus(ctx, h.reg.ID, StatusNeedsAuth, credentialRejectedHint)
		return nil, fmt.Errorf("mcp: %s: %w", credentialRejectedHint, err)
	}), nil
}

type tokenSourceFunc func() (*oauth2.Token, error)

func (f tokenSourceFunc) Token() (*oauth2.Token, error) { return f() }

// Authorize handles an eligible 401/403. It never starts an interactive flow
// from a tool call: the status flips to needs_auth and the caller gets a fixed
// recovery hint. Always returning non-nil prevents the transport's retry from
// looping (#1196).
func (h *oauthSession) Authorize(ctx context.Context, req *http.Request, resp *http.Response) error {
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	reason := "credential rejected by server"
	if challenges, err := oauthex.ParseWWWAuthenticate(resp.Header[http.CanonicalHeaderKey("WWW-Authenticate")]); err == nil {
		if code := challengeError(challenges); code != "" {
			reason = "credential rejected by server: " + code
		}
	}
	_ = h.svc.SetStatus(ctx, h.reg.ID, StatusNeedsAuth, reason)
	return fmt.Errorf("mcp: %s", credentialRejectedHint)
}

// challengeError extracts the RFC 6750 error code from a challenge. Only the
// three codes the RFC defines are persisted: the header is remote-controlled
// text and must not reach status_error verbatim.
func challengeError(challenges []oauthex.Challenge) string {
	for _, c := range challenges {
		switch code := c.Params["error"]; code {
		case "invalid_request", "invalid_token", "insufficient_scope":
			return code
		}
	}
	return ""
}

// scopesFromChallenges collects space-separated scope values from Bearer
// challenges (mirrors the SDK's own extraction).
func scopesFromChallenges(challenges []oauthex.Challenge) []string {
	var out []string
	for _, c := range challenges {
		if c.Scheme != "bearer" {
			continue
		}
		for scope := range strings.FieldsSeq(c.Params["scope"]) {
			if scope != "" {
				out = append(out, scope)
			}
		}
	}
	return out
}
