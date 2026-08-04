package web

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSPAHandlerCachesFingerprintedAssets(t *testing.T) {
	assetPath := firstEmbeddedAsset(t)

	req := httptest.NewRequest(http.MethodGet, "/"+assetPath, nil)
	rr := httptest.NewRecorder()
	SPAHandler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if got := rr.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Fatalf("Cache-Control = %q, want immutable asset cache", got)
	}
}

func TestSPAHandlerDoesNotCacheHTML(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/settings/credentials", nil)
	rr := httptest.NewRecorder()
	SPAHandler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if got := rr.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("Cache-Control = %q, want no-cache", got)
	}
}

func firstEmbeddedAsset(t *testing.T) string {
	t.Helper()
	entries, err := fs.ReadDir(staticFS, "static/dist/assets")
	if err != nil {
		t.Skipf("SPA not built, no embedded assets: %v", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".js") {
			return "assets/" + entry.Name()
		}
	}
	t.Skip("SPA not built, no embedded JS asset found")
	return ""
}

// A long-lived cache entry for the service worker would pin stale offline
// behavior in every browser that fetched it, with no way to recover remotely.
func TestSPAHandlerDoesNotCacheServiceWorker(t *testing.T) {
	if _, err := staticFS.ReadFile("static/dist/sw.js"); err != nil {
		t.Skipf("SPA not built, no embedded service worker: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/sw.js", nil)
	rr := httptest.NewRecorder()
	SPAHandler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if got := rr.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("Cache-Control = %q, want no-cache", got)
	}
}
