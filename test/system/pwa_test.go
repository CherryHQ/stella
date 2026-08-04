//go:build system

package system

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// testPWAAssetsAnonymous proves the install contract over the wire: a browser
// that has never logged in can still fetch the progressive-web-app manifest,
// service worker, and icons. Those requests carry no session, so if the auth
// middleware redirects them to /login the browser rejects the manifest and the
// worker on their content type and never offers to install the Web UI. Only the
// full chain — root mux, capability guard, auth middleware, SPA handler — can
// answer whether that holds, which is why this is a system journey.
//
// The assertion is deliberately "not redirected to /login" rather than a
// content check: `mise run build` compiles the binary without building the SPA,
// so in a fresh checkout the handler serves its fallback shell instead of the
// real files. The auth contract is the invariant; the payload is not.
func (h *harness) testPWAAssetsAnonymous(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// No cookie jar and no redirect following: a 302 must surface as a 302
	// rather than being replayed against /login.
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}

	for _, path := range []string{
		"/sw.js",
		"/site.webmanifest",
		"/favicon.svg",
		"/favicon-32x32.png",
		"/apple-touch-icon.png",
		"/icon-192.png",
		"/icon-512.png",
	} {
		code, location := anonymousGet(t, ctx, client, h.baseURL+path)
		if code != http.StatusOK {
			t.Errorf("anonymous GET %s = %d (Location %q), want %d: the browser cannot install the Web UI when this path needs a session\n%s",
				path, code, location, http.StatusOK, h.proc.logTail(40))
		}
	}

	// Control: the exemption must stay narrow. An ordinary page route still
	// sends a logged-out visitor to the login page.
	code, location := anonymousGet(t, ctx, client, h.baseURL+"/agents")
	if code != http.StatusFound || location != "/login" {
		t.Fatalf("anonymous GET /agents = %d (Location %q), want %d to /login\n%s",
			code, location, http.StatusFound, h.proc.logTail(40))
	}
}

func anonymousGet(t *testing.T, ctx context.Context, client *http.Client, url string) (int, string) {
	t.Helper()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("build request for %s: %v", url, err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	return resp.StatusCode, resp.Header.Get("Location")
}
