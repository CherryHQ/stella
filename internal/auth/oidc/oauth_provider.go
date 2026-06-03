package oidc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/CherryHQ/stella/internal/auth"
)

type OAuthProvider struct {
	cfg    *OAuthConfig
	client *http.Client

	mu                      sync.Mutex
	cachedFeishuAppToken    string
	feishuAppTokenExpiresAt time.Time
	feishuAppTokenGroup     singleflight.Group
}

const (
	oauthHTTPTimeout          = 10 * time.Second
	feishuAppTokenRefreshSkew = 5 * time.Minute
)

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
	if p.cfg.Kind == "feishu" && p.cfg.FeishuProfileTokenEnabled {
		started := time.Now()
		appToken, cached, err := p.feishuAppToken(ctx)
		logOAuthTiming(p.cfg.ProviderName, "feishu_app_token", started, err, "cached", cached)
		if err != nil {
			return nil, err
		}

		started = time.Now()
		profile, err := p.exchangeFeishuProfileToken(ctx, code, appToken)
		if isFeishuInvalidAppTokenError(err) {
			p.clearFeishuAppToken(appToken)
			appToken, _, tokenErr := p.feishuAppToken(ctx)
			if tokenErr != nil {
				err = tokenErr
			} else {
				profile, err = p.exchangeFeishuProfileToken(ctx, code, appToken)
			}
		}
		logOAuthTiming(p.cfg.ProviderName, "feishu_profile_token", started, err)
		if err != nil {
			return nil, err
		}
		if err := p.checkAllowed(profile); err != nil {
			return nil, err
		}
		return profile.identity(p.cfg.ProviderName)
	}

	started := time.Now()
	token, err := p.exchangeCode(ctx, code, state.CodeVerifier)
	logOAuthTiming(p.cfg.ProviderName, "exchange_code", started, err)
	if err != nil {
		return nil, err
	}
	started = time.Now()
	profile, err := p.fetchProfile(ctx, token.AccessToken)
	logOAuthTiming(p.cfg.ProviderName, "fetch_profile", started, err)
	if err != nil {
		return nil, err
	}
	if err := p.checkAllowed(profile); err != nil {
		return nil, err
	}
	return profile.identity(p.cfg.ProviderName)
}

type oauthTokenResponse struct {
	Code             int    `json:"code"`
	AccessToken      string `json:"access_token"`
	TokenType        string `json:"token_type"`
	Scope            string `json:"scope"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
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

type feishuAppTokenResponse struct {
	Code           int                   `json:"code"`
	Msg            string                `json:"msg"`
	AppAccessToken string                `json:"app_access_token"`
	Expire         int64                 `json:"expire"`
	Data           feishuAppTokenPayload `json:"data"`
}

type feishuAppTokenPayload struct {
	AppAccessToken string `json:"app_access_token"`
	Expire         int64  `json:"expire"`
}

type feishuProfileTokenResponse struct {
	Code int            `json:"code"`
	Msg  string         `json:"msg"`
	Data map[string]any `json:"data"`
}

type feishuAPIError struct {
	Code int
	Msg  string
}

func (e *feishuAPIError) Error() string {
	return fmt.Sprintf("Feishu API error %d: %s", e.Code, e.Msg)
}

func (p *OAuthProvider) feishuAppToken(ctx context.Context) (string, bool, error) {
	if token := p.cachedValidFeishuAppToken(); token != "" {
		return token, true, nil
	}

	value, err, shared := p.feishuAppTokenGroup.Do("app-token", func() (any, error) {
		if token := p.cachedValidFeishuAppToken(); token != "" {
			return token, nil
		}
		fetchCtx, cancel := context.WithTimeout(context.Background(), oauthHTTPTimeout)
		defer cancel()
		token, expiresAt, err := p.fetchFeishuAppToken(fetchCtx)
		if err != nil {
			return "", err
		}
		p.mu.Lock()
		p.cachedFeishuAppToken = token
		p.feishuAppTokenExpiresAt = expiresAt
		p.mu.Unlock()
		return token, nil
	})
	if err != nil {
		return "", false, err
	}
	return value.(string), shared, nil
}

func (p *OAuthProvider) cachedValidFeishuAppToken() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cachedFeishuAppToken != "" && time.Now().Before(p.feishuAppTokenExpiresAt) {
		return p.cachedFeishuAppToken
	}
	return ""
}

func (p *OAuthProvider) clearFeishuAppToken(token string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if token == "" || p.cachedFeishuAppToken == token {
		p.cachedFeishuAppToken = ""
		p.feishuAppTokenExpiresAt = time.Time{}
	}
}

func feishuAppTokenExpiresAt(now time.Time, expiresIn int64) time.Time {
	if expiresIn <= 0 {
		expiresIn = int64((time.Hour + feishuAppTokenRefreshSkew).Seconds())
	}
	expiresInDuration := time.Duration(expiresIn) * time.Second
	if expiresInDuration <= feishuAppTokenRefreshSkew {
		return now.Add(expiresInDuration / 2)
	}
	return now.Add(expiresInDuration - feishuAppTokenRefreshSkew)
}

func isFeishuInvalidAppTokenError(err error) bool {
	var apiErr *feishuAPIError
	if !errors.As(err, &apiErr) {
		return false
	}
	switch apiErr.Code {
	case 99991663, 99991668:
		return true
	default:
		return false
	}
}

func (p *OAuthProvider) fetchFeishuAppToken(ctx context.Context) (string, time.Time, error) {
	now := time.Now()
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(map[string]string{
		"app_id":     p.cfg.ClientID,
		"app_secret": p.cfg.ClientSecret,
	}); err != nil {
		return "", time.Time{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.FeishuAppTokenURL, &buf)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("oauth login: build Feishu app token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Accept", "application/json")

	var out feishuAppTokenResponse
	if err := p.doJSON(req, &out); err != nil {
		return "", time.Time{}, fmt.Errorf("oauth login: fetch Feishu app token: %w", err)
	}
	if out.Code != 0 {
		return "", time.Time{}, fmt.Errorf("oauth login: Feishu app token error %d: %s", out.Code, out.Msg)
	}
	token := out.AppAccessToken
	if token == "" {
		token = out.Data.AppAccessToken
	}
	if token == "" {
		return "", time.Time{}, errors.New("oauth login: Feishu app token response missing app_access_token")
	}
	expiresIn := out.Expire
	if expiresIn == 0 {
		expiresIn = out.Data.Expire
	}
	return token, feishuAppTokenExpiresAt(now, expiresIn), nil
}

func (p *OAuthProvider) exchangeFeishuProfileToken(ctx context.Context, code, appToken string) (*oauthProfile, error) {
	var buf bytes.Buffer
	// Feishu's v1 profile-bearing token endpoint is app-token authenticated
	// and does not define a code_verifier field in its public schema. The
	// callback state check above remains the CSRF boundary for this flow.
	if err := json.NewEncoder(&buf).Encode(map[string]string{
		"grant_type": "authorization_code",
		"code":       code,
	}); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.FeishuProfileTokenURL, &buf)
	if err != nil {
		return nil, fmt.Errorf("oauth login: build Feishu profile token request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+appToken)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Accept", "application/json")

	var out feishuProfileTokenResponse
	if err := p.doJSON(req, &out); err != nil {
		return nil, fmt.Errorf("oauth login: exchange Feishu profile token: %w", err)
	}
	if out.Code != 0 {
		return nil, fmt.Errorf("oauth login: Feishu profile token: %w", &feishuAPIError{Code: out.Code, Msg: out.Msg})
	}
	return feishuProfileFromClaims(out.Data), nil
}

func (p *OAuthProvider) fetchFeishuProfile(ctx context.Context, accessToken string) (*oauthProfile, error) {
	var out feishuUserInfoResponse
	if err := p.getBearerJSON(ctx, p.cfg.UserInfoURL, accessToken, &out); err != nil {
		return nil, fmt.Errorf("oauth login: fetch Feishu user info: %w", err)
	}
	if out.Code != 0 {
		return nil, fmt.Errorf("oauth login: Feishu user info error %d: %s", out.Code, out.Msg)
	}
	return feishuProfileFromClaims(out.Data), nil
}

func feishuProfileFromClaims(claims map[string]any) *oauthProfile {
	if claims == nil {
		claims = make(map[string]any)
	}
	profile := profileFromClaims(claims)
	profile.Subject = firstClaim(claims, "union_id", "open_id", "user_id")
	profile.Email = firstClaim(claims, "email", "enterprise_email")
	profile.EmailVerified = true
	profile.Name = firstClaim(claims, "name", "en_name")
	profile.AvatarURL = firstClaim(claims, "avatar_url", "avatar_big", "avatar_middle", "avatar_thumb")
	profile.TenantKey = firstClaim(claims, "tenant_key")
	if profile.Email == "" && profile.Subject != "" {
		profile.Email = syntheticFeishuEmail(profile.Subject, profile.TenantKey)
		claims["email_synthetic"] = true
	}
	return profile
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
		return fmt.Errorf("%s", resp.Status)
	}
	if err := json.Unmarshal(data, out); err != nil {
		return err
	}
	return nil
}

func logOAuthTiming(provider, step string, started time.Time, err error, attrs ...any) {
	fields := append([]any{
		"provider", provider,
		"step", step,
		"dur", time.Since(started),
	}, attrs...)
	if err != nil {
		fields = append(fields, "error", err)
		slog.Warn("oauth: callback step failed", fields...)
		return
	}
	slog.Info("oauth: callback step completed", fields...)
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

func syntheticFeishuEmail(subject, tenantKey string) string {
	subject = emailLocalPart(subject)
	if tenantKey == "" {
		return subject + "@feishu.local"
	}
	return subject + "@" + emailDomainLabel(tenantKey) + ".feishu.local"
}

func emailLocalPart(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), ".-")
	if out == "" {
		return "feishu-user"
	}
	return out
}

func emailDomainLabel(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "tenant"
	}
	return out
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
