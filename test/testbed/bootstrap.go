package testbed

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	credentialsFilename = "testbed-credentials.json"
	readyPollInterval   = 100 * time.Millisecond
	provisioningTTL     = 5 * time.Minute
)

type bootstrapConfig struct {
	BaseURL string
	Home    string
	// DatabaseURL is the testbed's own embedded PostgreSQL DSN, recorded so
	// e2e scripts can assert on rows directly. Empty when not started by the
	// supervisor.
	DatabaseURL string
	Client      *http.Client
	Now         func() time.Time
}

type credentials struct {
	Version     int    `json:"version"`
	BaseURL     string `json:"base_url"`
	DatabaseURL string `json:"database_url,omitempty"`
	Admin       struct {
		ID       string `json:"id"`
		Email    string `json:"email"`
		Role     string `json:"role"`
		Password string `json:"password"`
		Token    string `json:"token"`
	} `json:"admin"`
	User struct {
		ID    string `json:"id"`
		Email string `json:"email"`
		Role  string `json:"role"`
		Token string `json:"token"`
	} `json:"user"`
	FakeModel struct {
		ProviderID string `json:"provider_id,omitempty"`
		BaseURL    string `json:"base_url,omitempty"`
	} `json:"fake_model,omitempty"`
}

type authIdentity struct {
	ID      string `json:"id"`
	Email   string `json:"email"`
	Role    string `json:"role"`
	IsAdmin bool   `json:"is_admin"`
}

func bootstrap(ctx context.Context, cfg bootstrapConfig) (path string, reused bool, err error) {
	baseURL, err := normalizeBaseURL(cfg.BaseURL)
	if err != nil {
		return "", false, err
	}
	if cfg.Home == "" {
		return "", false, errors.New("STELLA_HOME is required")
	}
	path = filepath.Join(cfg.Home, credentialsFilename)
	existing, found, err := loadCredentials(path)
	if err != nil {
		return "", false, err
	}
	if found && existing.BaseURL != baseURL {
		return "", false, fmt.Errorf("credentials already exist for %s; refusing to use them for %s", existing.BaseURL, baseURL)
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	client := cfg.Client
	if client == nil {
		jar, err := cookiejar.New(nil)
		if err != nil {
			return "", false, fmt.Errorf("create session cookie jar: %w", err)
		}
		client = &http.Client{Jar: jar}
	}
	if err := waitReady(ctx, client, baseURL); err != nil {
		return "", false, err
	}
	if found {
		if err := validateCredentials(ctx, baseURL, existing); err != nil {
			return "", false, fmt.Errorf("existing credentials are not usable; refusing to overwrite them (start with a fresh STELLA_HOME): %w", err)
		}
		return path, true, nil
	}

	runID, err := randomString(12)
	if err != nil {
		return "", false, fmt.Errorf("generate fixture identity: %w", err)
	}
	// Local registration canonicalizes email to lowercase; keep the generated
	// identity canonical so the response can be verified exactly.
	runID = strings.ToLower(runID)
	password, err := randomPassword()
	if err != nil {
		return "", false, fmt.Errorf("generate administrator password: %w", err)
	}
	adminEmail := "testbed-admin-" + runID + "@stella.test"
	userEmail := "testbed-user-" + runID + "@stella.test"

	if err := postJSON(ctx, client, baseURL, "/api/auth/local/register", map[string]string{
		"name": "Testbed Admin " + runID, "email": adminEmail, "password": password, "confirm_password": password,
	}, http.StatusOK, nil); err != nil {
		return "", false, fmt.Errorf("register first local administrator: %w", err)
	}
	var adminIdentity authIdentity
	if err := getJSON(ctx, client, baseURL, "/api/auth/me", "", http.StatusOK, &adminIdentity); err != nil {
		return "", false, fmt.Errorf("read administrator identity: %w", err)
	}
	if adminIdentity.ID == "" || adminIdentity.Email != adminEmail || adminIdentity.Role != "admin" || !adminIdentity.IsAdmin {
		return "", false, errors.New("first local account did not become administrator")
	}

	var adminPAT struct {
		Token string `json:"token"`
	}
	if err := postJSON(ctx, client, baseURL, "/api/users/me/tokens", map[string]string{"name": "testbed-admin-" + runID}, http.StatusCreated, &adminPAT); err != nil {
		return "", false, fmt.Errorf("create administrator PAT: %w", err)
	}
	if adminPAT.Token == "" {
		return "", false, errors.New("create administrator PAT: response omitted token")
	}

	var provisioning struct {
		Token             string `json:"token"`
		ProvisioningToken struct {
			ID string `json:"id"`
		} `json:"provisioning_token"`
	}
	if err := postJSON(ctx, client, baseURL, "/api/admin/provisioning-tokens", map[string]string{
		"name": "testbed-provisioning-" + runID, "expires_at": now().UTC().Add(provisioningTTL).Format(time.RFC3339),
	}, http.StatusCreated, &provisioning); err != nil {
		return "", false, fmt.Errorf("create short-lived provisioning token: %w", err)
	}
	if provisioning.Token == "" || provisioning.ProvisioningToken.ID == "" {
		return "", false, errors.New("create provisioning token: response omitted token or id")
	}
	// Never leave a provisioning capability behind, including on a later error.
	revoked := false
	defer func() {
		if !revoked {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = deletePath(cleanupCtx, client, baseURL, "/api/admin/provisioning-tokens/"+url.PathEscape(provisioning.ProvisioningToken.ID), http.StatusNoContent)
		}
	}()

	var provisioned struct {
		Token           string `json:"token"`
		ProvisionedUser struct {
			ID       string `json:"id"`
			Role     string `json:"role"`
			IsActive bool   `json:"is_active"`
		} `json:"provisioned_user"`
	}
	provisioningClient := &http.Client{}
	if err := postJSONWithBearer(ctx, provisioningClient, baseURL, "/api/provisioned-users", provisioning.Token, map[string]string{
		"external_id": "testbed-user-" + runID, "email": userEmail, "name": "Testbed User " + runID,
	}, http.StatusCreated, &provisioned); err != nil {
		return "", false, fmt.Errorf("create passwordless user: %w", err)
	}
	if provisioned.Token == "" || provisioned.ProvisionedUser.ID == "" || provisioned.ProvisionedUser.Role != "user" || !provisioned.ProvisionedUser.IsActive {
		return "", false, errors.New("create passwordless user: response is not an active user credential")
	}

	if err := deletePath(ctx, client, baseURL, "/api/admin/provisioning-tokens/"+url.PathEscape(provisioning.ProvisioningToken.ID), http.StatusNoContent); err != nil {
		return "", false, fmt.Errorf("revoke provisioning token: %w", err)
	}
	revoked = true

	var userIdentity authIdentity
	if err := getJSON(ctx, &http.Client{}, baseURL, "/api/auth/me", provisioned.Token, http.StatusOK, &userIdentity); err != nil {
		return "", false, fmt.Errorf("read passwordless user identity: %w", err)
	}
	if userIdentity.ID == "" || userIdentity.Email != userEmail || userIdentity.Role != "user" || userIdentity.IsAdmin {
		return "", false, errors.New("passwordless PAT did not resolve to the expected user")
	}

	creds := credentials{Version: 1, BaseURL: baseURL, DatabaseURL: cfg.DatabaseURL}
	creds.Admin.ID, creds.Admin.Email, creds.Admin.Role = adminIdentity.ID, adminEmail, adminIdentity.Role
	creds.Admin.Password, creds.Admin.Token = password, adminPAT.Token
	creds.User.ID, creds.User.Email, creds.User.Role, creds.User.Token = userIdentity.ID, userEmail, userIdentity.Role, provisioned.Token
	if err := validateCredentials(ctx, baseURL, creds); err != nil {
		return "", false, fmt.Errorf("validate generated credentials: %w", err)
	}
	if err := writeCredentials(path, creds); err != nil {
		return "", false, err
	}
	return path, false, nil
}

func normalizeBaseURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", errors.New("--base-url must be an absolute HTTP URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", errors.New("--base-url must use http or https")
	}
	if u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("--base-url must not contain credentials, a path, query, or fragment")
	}
	u.Path = ""
	return strings.TrimRight(u.String(), "/"), nil
}

func waitReady(ctx context.Context, client *http.Client, baseURL string) error {
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/readyz", nil)
		if err != nil {
			return fmt.Errorf("build readiness request: %w", err)
		}
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("server did not become ready: %w", ctx.Err())
		case <-time.After(readyPollInterval):
		}
	}
}

func postJSON(ctx context.Context, client *http.Client, baseURL, path string, body any, want int, out any) error {
	return doJSON(ctx, client, http.MethodPost, baseURL, path, "", body, want, out)
}

func postJSONWithBearer(ctx context.Context, client *http.Client, baseURL, path, token string, body any, want int, out any) error {
	return doJSON(ctx, client, http.MethodPost, baseURL, path, token, body, want, out)
}

func getJSON(ctx context.Context, client *http.Client, baseURL, path, token string, want int, out any) error {
	return doJSON(ctx, client, http.MethodGet, baseURL, path, token, nil, want, out)
}

func doJSON(ctx context.Context, client *http.Client, method, baseURL, path, token string, body any, want int, out any) error {
	var requestBody io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		requestBody = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, baseURL+path, requestBody)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != want {
		return fmt.Errorf("unexpected HTTP status %d", resp.StatusCode)
	}
	if out != nil {
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}

func deletePath(ctx context.Context, client *http.Client, baseURL, path string, want int) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != want {
		return fmt.Errorf("unexpected HTTP status %d", resp.StatusCode)
	}
	return nil
}

func validateCredentials(ctx context.Context, baseURL string, creds credentials) error {
	client := &http.Client{} // Jar-less: an invalid PAT must never fall back to a session.
	for label, want := range map[string]struct {
		token string
		id    string
		email string
		role  string
		admin bool
	}{
		"administrator": {creds.Admin.Token, creds.Admin.ID, creds.Admin.Email, creds.Admin.Role, true},
		"user":          {creds.User.Token, creds.User.ID, creds.User.Email, creds.User.Role, false},
	} {
		var got authIdentity
		if err := getJSON(ctx, client, baseURL, "/api/auth/me", want.token, http.StatusOK, &got); err != nil {
			return fmt.Errorf("%s PAT authentication failed: %w", label, err)
		}
		if got.ID != want.id || got.Email != want.email || got.Role != want.role || got.IsAdmin != want.admin {
			return fmt.Errorf("%s PAT resolved to an unexpected identity", label)
		}
	}
	return nil
}

func randomString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func randomPassword() (string, error) {
	s, err := randomString(24)
	if err != nil {
		return "", err
	}
	return "At1!" + s, nil
}

func loadCredentials(path string) (credentials, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return credentials{}, false, nil
	}
	if err != nil {
		return credentials{}, false, fmt.Errorf("inspect credentials file: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return credentials{}, false, errors.New("credentials path must be a regular file")
	}
	if info.Mode().Perm() != 0o600 {
		return credentials{}, false, errors.New("credentials file must have mode 0600")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return credentials{}, false, fmt.Errorf("read credentials file: %w", err)
	}
	var creds credentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return credentials{}, false, errors.New("credentials file is invalid; refusing to overwrite it")
	}
	if creds.Version != 1 || creds.BaseURL == "" || creds.Admin.ID == "" || creds.Admin.Email == "" || creds.Admin.Role != "admin" || creds.Admin.Password == "" || creds.Admin.Token == "" || creds.User.ID == "" || creds.User.Email == "" || creds.User.Role != "user" || creds.User.Token == "" {
		return credentials{}, false, errors.New("credentials file is incomplete; refusing to overwrite it")
	}
	return creds, true, nil
}

func writeCredentials(path string, creds credentials) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create STELLA_HOME: %w", err)
	}
	if _, found, err := loadCredentials(path); err != nil {
		return err
	} else if found {
		return errors.New("credentials already exist; refusing to overwrite them")
	}
	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return fmt.Errorf("encode credentials: %w", err)
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".testbed-credentials-*")
	if err != nil {
		return fmt.Errorf("create credentials temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("restrict credentials temp file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write credentials: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync credentials: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close credentials: %w", err)
	}
	// Link is an atomic, no-replace publication primitive on one filesystem. It
	// prevents a concurrent bootstrap from replacing a credential artifact.
	if err := os.Link(tmpPath, path); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return errors.New("credentials appeared during bootstrap; refusing to overwrite them")
		}
		return fmt.Errorf("publish credentials: %w", err)
	}
	return nil
}
