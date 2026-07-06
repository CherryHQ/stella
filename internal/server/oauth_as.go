package server

import (
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"strings"

	zitoidc "github.com/zitadel/oidc/v3/pkg/oidc"

	"github.com/CherryHQ/stella/internal/credential"
	"github.com/CherryHQ/stella/internal/oidc"
)

// OAuth2 authorization-server HTTP surface (issue #613). These are NON-/api,
// browser/OAuth-client-facing endpoints, hand-registered in registerStaticRoutes
// (there is no OpenAPI spec for the OAuth2 protocol wire format). /oauth/authorize
// stays behind authMiddleware so it has a Stella session; /oauth/token is
// auth-exempt (it authenticates the client itself).

// parseAuthorizeRequest reads the OAuth parameters via FormValue so a single
// path serves both the GET consent screen (params in the query) and the approval
// POST (params echoed as hidden form fields) -- ParseForm merges query and body,
// so the request no longer depends on the browser preserving the query string on
// submit.
func parseAuthorizeRequest(r *http.Request) oidc.AuthorizeRequest {
	return oidc.AuthorizeRequest{
		ClientID:            r.FormValue("client_id"),
		RedirectURI:         r.FormValue("redirect_uri"),
		ResponseType:        r.FormValue("response_type"),
		Scopes:              splitScopes(r.FormValue("scope")),
		State:               r.FormValue("state"),
		CodeChallenge:       r.FormValue("code_challenge"),
		CodeChallengeMethod: r.FormValue("code_challenge_method"),
	}
}

func splitScopes(s string) []string {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return nil
	}
	return fields
}

// handleOAuthAuthorize renders the consent screen (GET) and issues the
// authorization code on approval (POST). Both require a logged-in Stella user;
// the SameSite=Lax session cookie is what protects the approval POST from CSRF
// (a cross-site POST carries no session and is redirected to login).
func (s *Server) handleOAuthAuthorize(w http.ResponseWriter, r *http.Request) {
	if s.oauthAS == nil {
		http.Error(w, "oauth authorization server is not enabled", http.StatusNotImplemented)
		return
	}
	info := UserFromContext(r.Context())
	if info == nil || info.UserID == "" {
		// Behind authMiddleware this is unreachable, but fail closed.
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	req := parseAuthorizeRequest(r)
	authCtx, err := s.oauthAS.Authorize(r.Context(), req)
	if err != nil {
		var redirErr *oidc.RedirectError
		if errors.As(err, &redirErr) {
			redirectAuthError(w, r, redirErr)
			return
		}
		// Bad client / redirect_uri: never redirect, show the error.
		s.renderConsentError(w, err.Error())
		return
	}

	if r.Method == http.MethodPost {
		if r.FormValue("consent") != "approve" {
			redirectAuthError(w, r, &oidc.RedirectError{
				RedirectURI: req.RedirectURI, State: req.State,
				Code: "access_denied", Description: "user denied the request",
			})
			return
		}
		code, err := s.oauthAS.IssueCode(r.Context(), info.UserID, req, authCtx.Scopes)
		if err != nil {
			s.renderConsentError(w, "failed to issue authorization code")
			return
		}
		u := buildRedirect(req.RedirectURI, url.Values{
			"code":  {code},
			"state": {req.State},
		})
		http.Redirect(w, r, u, http.StatusFound)
		return
	}

	s.renderConsent(w, info, authCtx, req)
}

// handleOAuthToken is the token endpoint: authorization_code and refresh_token.
// It authenticates the client, not the user, so it is auth-exempt.
func (s *Server) handleOAuthToken(w http.ResponseWriter, r *http.Request) {
	if s.oauthAS == nil {
		writeTokenError(w, zitoidc.ErrServerError().WithDescription("oauth authorization server is not enabled"))
		return
	}
	// Throttle by IP: the token endpoint is auth-exempt and lets a caller guess
	// client secrets, authorization codes, and refresh-token secrets, so it needs
	// the same brute-force ceiling as the login endpoints.
	ip := clientIP(r)
	if err := s.rateLimiter.CheckIP(ip); err != nil {
		writeTokenError(w, zitoidc.ErrSlowDown().WithDescription("%s", err.Error()))
		return
	}
	if err := r.ParseForm(); err != nil {
		writeTokenError(w, zitoidc.ErrInvalidRequest().WithDescription("malformed request body"))
		return
	}
	clientID, clientSecret := clientCredentials(r)
	res, err := s.oauthAS.Exchange(r.Context(), oidc.TokenRequest{
		GrantType:    r.PostForm.Get("grant_type"),
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Code:         r.PostForm.Get("code"),
		RedirectURI:  r.PostForm.Get("redirect_uri"),
		CodeVerifier: r.PostForm.Get("code_verifier"),
		RefreshToken: r.PostForm.Get("refresh_token"),
		Scope:        splitScopes(r.PostForm.Get("scope")),
	})
	if err != nil {
		var oerr *zitoidc.Error
		if errors.As(err, &oerr) {
			// Count only credential-guessing failures toward the IP ceiling;
			// server_error is our fault, not an attacker probing.
			if oerr.ErrorType == zitoidc.InvalidClient || oerr.ErrorType == zitoidc.InvalidGrant {
				s.rateLimiter.RecordIPAttempt(ip)
			}
			writeTokenError(w, oerr)
			return
		}
		writeTokenError(w, zitoidc.ErrServerError().WithParent(err))
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	writeJSON(w, http.StatusOK, &zitoidc.AccessTokenResponse{
		AccessToken:  res.AccessToken,
		TokenType:    res.TokenType,
		RefreshToken: res.RefreshToken,
		ExpiresIn:    uint64(res.ExpiresIn),
		Scope:        res.Scopes,
	})
}

// clientCredentials extracts client_id/client_secret from HTTP Basic auth
// (preferred) or the form body (client_secret_post).
func clientCredentials(r *http.Request) (id, secret string) {
	if u, p, ok := r.BasicAuth(); ok {
		// Basic-auth values are form-url-encoded per RFC 6749 2.3.1.
		if du, err := url.QueryUnescape(u); err == nil {
			u = du
		}
		if dp, err := url.QueryUnescape(p); err == nil {
			p = dp
		}
		return u, p
	}
	return r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
}

func writeTokenError(w http.ResponseWriter, e *zitoidc.Error) {
	status := http.StatusBadRequest
	switch e.ErrorType {
	case "invalid_client":
		status = http.StatusUnauthorized
	case "server_error":
		status = http.StatusInternalServerError
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, status, e)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func redirectAuthError(w http.ResponseWriter, r *http.Request, e *oidc.RedirectError) {
	http.Redirect(w, r, buildRedirect(e.RedirectURI, url.Values{
		"error":             {e.Code},
		"error_description": {e.Description},
		"state":             {e.State},
	}), http.StatusFound)
}

// buildRedirect appends non-empty query params to a redirect URI, preserving any
// existing query. State is omitted when empty.
func buildRedirect(base string, params url.Values) string {
	u, err := url.Parse(base)
	if err != nil {
		return base
	}
	q := u.Query()
	for k, vs := range params {
		for _, v := range vs {
			if v == "" {
				continue
			}
			q.Set(k, v)
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// ---- consent rendering ----

type consentScope struct {
	Scope       string
	Description string
}

type consentData struct {
	ClientName string
	UserEmail  string
	Scopes     []consentScope
	Params     map[string]string
}

func (s *Server) renderConsent(w http.ResponseWriter, info *AuthInfo, ctx *oidc.AuthorizeContext, req oidc.AuthorizeRequest) {
	data := consentData{
		ClientName: ctx.Client.Name,
		UserEmail:  info.Email,
		Params: map[string]string{
			"client_id":             req.ClientID,
			"redirect_uri":          req.RedirectURI,
			"response_type":         req.ResponseType,
			"scope":                 strings.Join(req.Scopes, " "),
			"state":                 req.State,
			"code_challenge":        req.CodeChallenge,
			"code_challenge_method": req.CodeChallengeMethod,
		},
	}
	if data.UserEmail == "" {
		data.UserEmail = info.Username
	}
	descByResource := map[string]string{}
	for _, sc := range credential.Catalog() {
		descByResource[sc.Resource] = sc.Description
	}
	for _, sc := range ctx.Scopes {
		resource, _, _ := strings.Cut(sc, ":")
		data.Scopes = append(data.Scopes, consentScope{Scope: sc, Description: descByResource[resource]})
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := consentTmpl.Execute(w, data); err != nil {
		s.log.Warn("oauth: render consent failed", "error", err)
	}
}

func (s *Server) renderConsentError(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusBadRequest)
	_ = consentErrorTmpl.Execute(w, map[string]string{"Message": msg})
}

var consentTmpl = template.Must(template.New("consent").Parse(`<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Authorize application</title>
<style>
body{font-family:system-ui,sans-serif;background:#0b0d12;color:#e6e8eb;display:flex;min-height:100vh;margin:0;align-items:center;justify-content:center}
.card{background:#151922;border:1px solid #262c38;border-radius:12px;max-width:420px;width:92%;padding:28px}
h1{font-size:18px;margin:0 0 4px}
p.sub{color:#9aa4b2;font-size:13px;margin:0 0 20px}
ul{list-style:none;padding:0;margin:0 0 24px}
li{padding:10px 12px;background:#0f131b;border:1px solid #222836;border-radius:8px;margin-bottom:8px}
li .s{font-family:ui-monospace,monospace;font-size:12px;color:#8ab4f8}
li .d{font-size:13px;color:#c3cad4}
.row{display:flex;gap:10px}
button{flex:1;padding:10px;border-radius:8px;border:0;font-size:14px;cursor:pointer}
.approve{background:#3b82f6;color:#fff}
.deny{background:#242b38;color:#e6e8eb}
.who{color:#9aa4b2;font-size:12px;margin-top:16px;text-align:center}
</style></head><body>
<div class="card">
<h1>Authorize {{.ClientName}}</h1>
<p class="sub">This application is requesting access to your Stella account.</p>
<ul>{{range .Scopes}}<li><div class="s">{{.Scope}}</div><div class="d">{{.Description}}</div></li>{{end}}</ul>
<form method="POST">
{{range $k, $v := .Params}}<input type="hidden" name="{{$k}}" value="{{$v}}">{{end}}
<div class="row">
<button class="deny" type="submit" name="consent" value="deny">Deny</button>
<button class="approve" type="submit" name="consent" value="approve">Allow</button>
</div>
</form>
<div class="who">Signed in as {{.UserEmail}}</div>
</div></body></html>`))

var consentErrorTmpl = template.Must(template.New("consentError").Parse(`<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8"><title>Authorization error</title>
<style>body{font-family:system-ui,sans-serif;background:#0b0d12;color:#e6e8eb;display:flex;min-height:100vh;margin:0;align-items:center;justify-content:center}
.card{background:#151922;border:1px solid #262c38;border-radius:12px;max-width:420px;width:92%;padding:28px;text-align:center}
h1{font-size:18px}</style></head><body>
<div class="card"><h1>Authorization error</h1><p>{{.Message}}</p></div></body></html>`))
