//go:build system

package system

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"
)

// testStartupAndAuth proves the wire-level authentication contract against the
// running binary: registration mints a real session, that session cookie alone
// authenticates, and a present-but-malformed Bearer is a hard deny that never
// falls back to the cookie. The distinction between "no Authorization header"
// (normal session auth) and "malformed Bearer" (fail closed) is the security
// property under test — conflating them would encode the wrong contract.
//
// /api/auth/me is the probe: it requires authentication (unlike /api/status,
// which is a public health endpoint reachable anonymously), so its 200/401
// answer directly reflects whether the request authenticated.
func (h *harness) testStartupAndAuth(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// Anonymous first: the cookie jar is empty until registration, so this proves
	// the unauthenticated 401 before any credential exists. It must run before
	// registration for the jar to be genuinely empty.
	if code := h.statusOf(t, ctx, h.newAuthedGet(t, ctx, nil)); code != http.StatusUnauthorized {
		t.Fatalf("GET /api/auth/me with no credentials = %d, want %d\n%s", code, http.StatusUnauthorized, h.proc.LogTail(40))
	}

	h.registerBootstrapUser(t, ctx)

	// Session cookie only, no Authorization header: normal session auth succeeds.
	if code := h.statusOf(t, ctx, h.newAuthedGet(t, ctx, nil)); code != http.StatusOK {
		t.Fatalf("GET /api/auth/me with session cookie = %d, want %d\n%s", code, http.StatusOK, h.proc.LogTail(40))
	}

	// A malformed Bearer alongside the valid session cookie must fail closed. Two
	// shapes are exercised: an empty Bearer ("Authorization: Bearer") and a
	// Bearer with an unrecognized token. Neither may fall back to the cookie,
	// which is why both must return 401 even though the cookie by itself works.
	for _, bearer := range []string{"Bearer", "Bearer not-a-real-token"} {
		req := h.newAuthedGet(t, ctx, map[string]string{"Authorization": bearer})
		if code := h.statusOf(t, ctx, req); code != http.StatusUnauthorized {
			t.Fatalf("GET /api/auth/me with session cookie + %q = %d, want %d (malformed Bearer must not fall back to cookie)\n%s",
				describeBearer(bearer), code, http.StatusUnauthorized, h.proc.LogTail(40))
		}
	}

	h.testPersonalAccessToken(t, ctx)
}

// testPersonalAccessToken proves the PAT bearer lifecycle end to end over the
// wire: a session mints a token, that token alone (no cookie) authenticates an
// ordinary API route, and revoking it makes the same bearer fail closed. It runs
// inside startup_and_auth because it reuses the bootstrap session already
// established above.
//
// The probe route is GET /api/agents. Its 200 vs 401 directly reflects whether
// the API-only bearer authenticated; domain authorization still applies to the
// caller after the credential boundary.
//
// Failure messages never echo the token or the Authorization header: only the
// PAT id (a non-secret handle) and status codes appear, so test output can never
// leak a live credential.
func (h *harness) testPersonalAccessToken(t *testing.T, ctx context.Context) {
	t.Helper()

	token, id := h.createPAT(t, ctx)

	// The token alone — carried on a jar-less client so only the Authorization
	// header can authenticate — must reach the API route.
	if code := h.bearerProbeStatus(t, ctx, token); code != http.StatusOK {
		t.Fatalf("GET /api/agents with PAT %s = %d, want %d (a valid bearer must authenticate on its own)\n%s",
			id, code, http.StatusOK, h.proc.LogTail(40))
	}

	h.revokePAT(t, ctx, id)

	// Same bearer, now revoked: a present-but-invalid credential is a hard deny
	// (401), never a silent fall-through to any other auth.
	if code := h.bearerProbeStatus(t, ctx, token); code != http.StatusUnauthorized {
		t.Fatalf("GET /api/agents with revoked PAT %s = %d, want %d (a revoked bearer must fail closed)\n%s",
			id, code, http.StatusUnauthorized, h.proc.LogTail(40))
	}
}

// createPAT mints a personal access token through the session-authenticated
// endpoint and returns its one-time plaintext and its id.
// The plaintext is returned only here and never again, matching the production
// contract; callers must not log it.
func (h *harness) createPAT(t *testing.T, ctx context.Context) (token, id string) {
	t.Helper()
	body := map[string]any{
		"name": "system-test-pat-" + h.runID,
	}
	resp := h.postJSON(t, ctx, "/api/users/me/tokens", body)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/users/me/tokens = %d, want %d\n%s", resp.StatusCode, http.StatusCreated, h.proc.LogTail(40))
	}
	var created struct {
		Token               string `json:"token"`
		PersonalAccessToken struct {
			Id string `json:"id"`
		} `json:"personal_access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode create-token response: %v", err)
	}
	if created.Token == "" {
		t.Fatal("create-token response has empty plaintext token")
	}
	if created.PersonalAccessToken.Id == "" {
		t.Fatal("create-token response has empty token id")
	}
	return created.Token, created.PersonalAccessToken.Id
}

// revokePAT deletes the PAT by id through the session-authenticated endpoint and
// asserts the 204 the contract specifies.
func (h *harness) revokePAT(t *testing.T, ctx context.Context, id string) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, h.baseURL+"/api/users/me/tokens/"+id, nil)
	if err != nil {
		t.Fatalf("build revoke-token request: %v", err)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("DELETE token %s: %v\n%s", id, err, h.proc.LogTail(40))
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE /api/users/me/tokens/%s = %d, want %d\n%s", id, resp.StatusCode, http.StatusNoContent, h.proc.LogTail(40))
	}
}

// bearerProbeStatus performs GET /api/agents authenticated only by the given
// bearer token and returns the status code. It uses a fresh jar-less client so
// no session cookie can mask the token's own outcome, and it never logs the
// token or the Authorization header — only the resulting status is observable.
func (h *harness) bearerProbeStatus(t *testing.T, ctx context.Context, token string) int {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.baseURL+"/api/agents", nil)
	if err != nil {
		t.Fatalf("build bearer probe request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	// Jar-less: only the Authorization header authenticates this request.
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		t.Fatalf("GET /api/agents (PAT bearer): %v\n%s", err, h.proc.LogTail(40))
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// registerBootstrapUser registers the first local user through the public
// endpoint and lets the cookie jar capture the returned session. It does not
// seed auth rows directly: the whole point of the journey is that the real
// registration path issues a usable session over the wire. The first account
// bootstraps without any registration env flag because the server always
// allows the very first user (BootstrapRegistration).
func (h *harness) registerBootstrapUser(t *testing.T, ctx context.Context) {
	t.Helper()
	body := map[string]string{
		"name":             "System Test " + h.runID,
		"email":            fmt.Sprintf("bootstrap-%s@system.test", h.runID),
		"password":         "system-test-" + h.runID,
		"confirm_password": "system-test-" + h.runID,
	}
	resp := h.postJSON(t, ctx, "/api/auth/local/register", body)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/auth/local/register = %d, want %d\n%s", resp.StatusCode, http.StatusOK, h.proc.LogTail(40))
	}
}

// newAuthedGet builds a GET /api/auth/me request with the given extra headers.
// The client's cookie jar supplies the session cookie automatically once
// registration has populated it.
func (h *harness) newAuthedGet(t *testing.T, ctx context.Context, headers map[string]string) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.baseURL+"/api/auth/me", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return req
}

// statusOf performs a request and returns its status code, closing the body.
func (h *harness) statusOf(t *testing.T, ctx context.Context, req *http.Request) int {
	t.Helper()
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", req.Method, req.URL.Path, err, h.proc.LogTail(40))
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// postJSON sends a JSON body and returns the response with its body open for
// the caller to inspect and close.
func (h *harness) postJSON(t *testing.T, ctx context.Context, path string, body any) *http.Response {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request body: %v", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v\n%s", path, err, h.proc.LogTail(40))
	}
	return resp
}

// describeBearer renders an Authorization value for failure messages without
// echoing any real token. The malformed values under test carry no secret, but
// keeping this centralized ensures test output never prints a live credential.
func describeBearer(header string) string {
	if header == "Bearer" {
		return "empty Bearer"
	}
	return "malformed Bearer"
}
