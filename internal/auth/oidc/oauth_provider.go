package oidc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/CherryHQ/stella/internal/auth"
)

type OAuthProvider struct {
	cfg    *OAuthConfig
	client *http.Client
}

const oauthHTTPTimeout = 10 * time.Second

func NewOAuthProvider(cfg *OAuthConfig) (*OAuthProvider, error) {
	return NewOAuthProviderWithClient(cfg, &http.Client{Timeout: oauthHTTPTimeout})
}

func NewOAuthProviderWithClient(cfg *OAuthConfig, client *http.Client) (*OAuthProvider, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if client == nil {
		client = &http.Client{Timeout: oauthHTTPTimeout}
	}
	return &OAuthProvider{cfg: cfg, client: client}, nil
}

func (p *OAuthProvider) Name() string { return p.cfg.ProviderName }

// ClientID returns the OAuth client_id used for this login provider.
func (p *OAuthProvider) ClientID() string { return p.cfg.ClientID }

func (p *OAuthProvider) LoginURL(_ context.Context, state auth.AuthState) (string, error) {
	challenge, method, err := pkceChallenge(state.CodeVerifier)
	if err != nil {
		return "", fmt.Errorf("oauth login: generate PKCE challenge: %w", err)
	}
	u, err := url.Parse(p.cfg.AuthURL)
	if err != nil {
		return "", fmt.Errorf("oauth login: parse auth URL: %w", err)
	}
	q := u.Query()
	q.Set("client_id", p.cfg.ClientID)
	q.Set("response_type", "code")
	q.Set("redirect_uri", p.cfg.RedirectURL)
	q.Set("state", state.State)
	if len(p.cfg.Scopes) > 0 {
		q.Set("scope", strings.Join(p.cfg.Scopes, " "))
	}
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", method)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func (p *OAuthProvider) HandleCallback(ctx context.Context, r *http.Request, state auth.AuthState) (*auth.ExternalIdentity, error) {
	if errParam := r.URL.Query().Get("error"); errParam != "" {
		return nil, fmt.Errorf("oauth login: provider returned error %q: %s", errParam, r.URL.Query().Get("error_description"))
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		return nil, errors.New("oauth login: missing code in callback")
	}
	token, err := p.exchangeCode(ctx, code, state.CodeVerifier)
	if err != nil {
		return nil, err
	}
	profile, err := p.fetchProfile(ctx, token.AccessToken)
	if err != nil {
		return nil, err
	}
	if err := p.checkAllowed(profile); err != nil {
		return nil, err
	}
	identity, err := profile.identity(p.cfg.ProviderName)
	if err != nil {
		return nil, err
	}
	identity.OAuthToken = &auth.OAuthToken{
		AccessToken:           token.AccessToken,
		RefreshToken:          token.RefreshToken,
		ExpiresIn:             token.ExpiresIn,
		RefreshTokenExpiresIn: token.RefreshTokenExpiresIn,
	}
	return identity, nil
}

type oauthTokenResponse struct {
	Code                  int    `json:"code"`
	AccessToken           string `json:"access_token"`
	RefreshToken          string `json:"refresh_token"`
	ExpiresIn             int    `json:"expires_in"`
	RefreshTokenExpiresIn int    `json:"refresh_token_expires_in"`
	TokenType             string `json:"token_type"`
	Scope                 string `json:"scope"`
	Error                 string `json:"error"`
	ErrorDescription      string `json:"error_description"`
}

func (p *OAuthProvider) exchangeCode(ctx context.Context, code, verifier string) (*oauthTokenResponse, error) {
	fields := map[string]string{
		"grant_type":    "authorization_code",
		"client_id":     p.cfg.ClientID,
		"client_secret": p.cfg.ClientSecret,
		"code":          code,
		"redirect_uri":  p.cfg.RedirectURL,
		"code_verifier": verifier,
	}
	req, err := p.tokenRequest(ctx, fields)
	if err != nil {
		return nil, err
	}
	var out oauthTokenResponse
	if err := p.doJSON(req, &out); err != nil {
		return nil, fmt.Errorf("oauth login: exchange code: %w", err)
	}
	if out.Code != 0 {
		return nil, fmt.Errorf("oauth login: token error %d %s: %s", out.Code, out.Error, out.ErrorDescription)
	}
	if out.Error != "" {
		return nil, fmt.Errorf("oauth login: token error %s: %s", out.Error, out.ErrorDescription)
	}
	if out.AccessToken == "" {
		return nil, errors.New("oauth login: token response missing access_token")
	}
	return &out, nil
}

func (p *OAuthProvider) tokenRequest(ctx context.Context, fields map[string]string) (*http.Request, error) {
	if p.cfg.TokenRequestStyle == "json" {
		var buf bytes.Buffer
		if err := json.NewEncoder(&buf).Encode(fields); err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.TokenURL, &buf)
		if err != nil {
			return nil, fmt.Errorf("oauth login: build token request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
		return req, nil
	}

	form := url.Values{}
	for k, v := range fields {
		form.Set(k, v)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("oauth login: build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	return req, nil
}

type oauthProfile struct {
	Subject       string
	Email         string
	EmailVerified bool
	Name          string
	AvatarURL     string
	TenantKey     string
	Claims        map[string]any
}

func (p *OAuthProvider) fetchProfile(ctx context.Context, accessToken string) (*oauthProfile, error) {
	switch p.cfg.Kind {
	case "feishu":
		return p.fetchFeishuProfile(ctx, accessToken)
	case "github":
		return p.fetchGitHubProfile(ctx, accessToken)
	default:
		return p.fetchGenericProfile(ctx, accessToken)
	}
}

func (p *OAuthProvider) fetchGenericProfile(ctx context.Context, accessToken string) (*oauthProfile, error) {
	var raw map[string]any
	if err := p.getBearerJSON(ctx, p.cfg.UserInfoURL, accessToken, &raw); err != nil {
		return nil, fmt.Errorf("oauth login: fetch user info: %w", err)
	}
	profile := profileFromClaims(raw)
	if p.cfg.RequireEmailVerified && !profile.EmailVerified {
		return nil, errors.New("oauth login: email verification was not confirmed by provider")
	}
	if !p.cfg.RequireEmailVerified && profile.Email != "" {
		profile.EmailVerified = true
	}
	return profile, nil
}

type feishuUserInfoResponse struct {
	Code int            `json:"code"`
	Msg  string         `json:"msg"`
	Data map[string]any `json:"data"`
}

func (p *OAuthProvider) fetchFeishuProfile(ctx context.Context, accessToken string) (*oauthProfile, error) {
	var out feishuUserInfoResponse
	if err := p.getBearerJSON(ctx, p.cfg.UserInfoURL, accessToken, &out); err != nil {
		return nil, fmt.Errorf("oauth login: fetch Feishu user info: %w", err)
	}
	if out.Code != 0 {
		return nil, fmt.Errorf("oauth login: Feishu user info error %d: %s", out.Code, out.Msg)
	}
	claims := out.Data
	profile := profileFromClaims(claims)
	profile.Subject = firstClaim(claims, "union_id", "open_id", "user_id")
	profile.Email = firstClaim(claims, "email", "enterprise_email")
	profile.EmailVerified = true
	profile.Name = firstClaim(claims, "name", "en_name")
	profile.AvatarURL = firstClaim(claims, "avatar_url", "avatar_big", "avatar_middle", "avatar_thumb")
	profile.TenantKey = firstClaim(claims, "tenant_key")
	if profile.Email == "" && profile.Subject != "" {
		profile.Email = auth.SyntheticFeishuEmail(profile.Subject, profile.TenantKey)
		claims["email_synthetic"] = true
	}
	return profile, nil
}

type githubEmail struct {
	Email    string `json:"email"`
	Primary  bool   `json:"primary"`
	Verified bool   `json:"verified"`
}

func (p *OAuthProvider) fetchGitHubProfile(ctx context.Context, accessToken string) (*oauthProfile, error) {
	var raw map[string]any
	if err := p.getBearerJSON(ctx, p.cfg.UserInfoURL, accessToken, &raw); err != nil {
		return nil, fmt.Errorf("oauth login: fetch GitHub user: %w", err)
	}
	profile := profileFromClaims(raw)
	profile.Subject = fmt.Sprint(raw["id"])
	profile.Email = ""
	profile.EmailVerified = false
	if p.cfg.UserEmailsURL != "" {
		var emails []githubEmail
		if err := p.getBearerJSON(ctx, p.cfg.UserEmailsURL, accessToken, &emails); err != nil {
			return nil, fmt.Errorf("oauth login: fetch GitHub emails: %w", err)
		}
		for _, e := range emails {
			if e.Primary && e.Verified {
				profile.Email = e.Email
				profile.EmailVerified = true
				break
			}
		}
		if profile.Email == "" {
			for _, e := range emails {
				if e.Verified {
					profile.Email = e.Email
					profile.EmailVerified = true
					break
				}
			}
		}
	}
	return profile, nil
}

func (p *OAuthProvider) getBearerJSON(ctx context.Context, rawURL, accessToken string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	return p.doJSON(req, out)
}

func (p *OAuthProvider) doJSON(req *http.Request, out any) error {
	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	if err := json.Unmarshal(data, out); err != nil {
		return err
	}
	return nil
}

func (p *OAuthProvider) checkAllowed(profile *oauthProfile) error {
	if len(p.cfg.AllowedTenantKeys) > 0 && !containsFold(p.cfg.AllowedTenantKeys, profile.TenantKey) {
		return fmt.Errorf("oauth login: tenant %q is not allowed", profile.TenantKey)
	}
	if len(p.cfg.AllowedEmailDomains) > 0 && !emailDomainAllowed(profile.Email, p.cfg.AllowedEmailDomains) {
		return errors.New("oauth login: email domain is not allowed")
	}
	return nil
}

func (p *oauthProfile) identity(providerName string) (*auth.ExternalIdentity, error) {
	if strings.TrimSpace(p.Subject) == "" {
		return nil, errors.New("oauth login: user info missing subject")
	}
	if strings.TrimSpace(p.Email) == "" {
		return nil, errors.New("oauth login: user info missing verified email")
	}
	if !p.EmailVerified {
		return nil, errors.New("oauth login: user info email is not verified")
	}
	return &auth.ExternalIdentity{
		Provider:  providerName,
		Subject:   p.Subject,
		Email:     strings.ToLower(strings.TrimSpace(p.Email)),
		Name:      p.Name,
		AvatarURL: p.AvatarURL,
		Claims:    p.Claims,
	}, nil
}

func profileFromClaims(claims map[string]any) *oauthProfile {
	return &oauthProfile{
		Subject:       firstClaim(claims, "sub", "id", "user_id", "open_id"),
		Email:         firstClaim(claims, "email"),
		EmailVerified: boolClaim(claims, "email_verified"),
		Name:          firstClaim(claims, "name", "login", "preferred_username"),
		AvatarURL:     firstClaim(claims, "picture", "avatar_url"),
		TenantKey:     firstClaim(claims, "tenant_key"),
		Claims:        claims,
	}
}

func boolClaim(claims map[string]any, name string) bool {
	v, ok := claims[name]
	if !ok {
		return false
	}
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return strings.EqualFold(x, "true")
	default:
		return false
	}
}

func firstClaim(claims map[string]any, names ...string) string {
	for _, name := range names {
		if v, ok := claims[name]; ok {
			switch x := v.(type) {
			case string:
				if strings.TrimSpace(x) != "" {
					return x
				}
			case float64:
				return fmt.Sprintf("%.0f", x)
			case int64:
				return fmt.Sprint(x)
			case int:
				return fmt.Sprint(x)
			}
		}
	}
	return ""
}

func containsFold(values []string, want string) bool {
	want = strings.TrimSpace(want)
	for _, v := range values {
		if strings.EqualFold(strings.TrimSpace(v), want) {
			return true
		}
	}
	return false
}

func emailDomainAllowed(email string, domains []string) bool {
	at := strings.LastIndex(email, "@")
	if at < 0 || at == len(email)-1 {
		return false
	}
	domain := strings.ToLower(strings.TrimSpace(email[at+1:]))
	for _, d := range domains {
		d = strings.ToLower(strings.TrimSpace(d))
		if d == "" {
			continue
		}
		if domain == d || strings.HasSuffix(domain, "."+d) {
			return true
		}
	}
	return false
}
