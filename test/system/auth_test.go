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
		t.Fatalf("GET /api/auth/me with no credentials = %d, want %d\n%s", code, http.StatusUnauthorized, h.proc.logTail(40))
	}

	h.registerBootstrapUser(t, ctx)

	// Session cookie only, no Authorization header: normal session auth succeeds.
	if code := h.statusOf(t, ctx, h.newAuthedGet(t, ctx, nil)); code != http.StatusOK {
		t.Fatalf("GET /api/auth/me with session cookie = %d, want %d\n%s", code, http.StatusOK, h.proc.logTail(40))
	}

	// A malformed Bearer alongside the valid session cookie must fail closed. Two
	// shapes are exercised: an empty Bearer ("Authorization: Bearer") and a
	// Bearer with an unrecognized token. Neither may fall back to the cookie,
	// which is why both must return 401 even though the cookie by itself works.
	for _, bearer := range []string{"Bearer", "Bearer not-a-real-token"} {
		req := h.newAuthedGet(t, ctx, map[string]string{"Authorization": bearer})
		if code := h.statusOf(t, ctx, req); code != http.StatusUnauthorized {
			t.Fatalf("GET /api/auth/me with session cookie + %q = %d, want %d (malformed Bearer must not fall back to cookie)\n%s",
				describeBearer(bearer), code, http.StatusUnauthorized, h.proc.logTail(40))
		}
	}
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
		t.Fatalf("POST /api/auth/local/register = %d, want %d\n%s", resp.StatusCode, http.StatusOK, h.proc.logTail(40))
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
		t.Fatalf("%s %s: %v\n%s", req.Method, req.URL.Path, err, h.proc.logTail(40))
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
		t.Fatalf("POST %s: %v\n%s", path, err, h.proc.logTail(40))
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
