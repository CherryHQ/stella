package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/CherryHQ/stella/internal/version"
)

func TestRedirectRoot(t *testing.T) {
	tests := []struct {
		name string
		info *AuthInfo
		want string
	}{
		{name: "unauthenticated", want: "/login"},
		{name: "user", info: &AuthInfo{UserID: "user-1"}, want: "/agents"},
		{name: "admin", info: &AuthInfo{UserID: "admin-1", IsAdmin: true}, want: "/agents"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.info != nil {
				req = req.WithContext(withAuthInfo(req.Context(), tt.info))
			}
			rec := httptest.NewRecorder()

			(&Server{}).redirectRoot(rec, req)

			if rec.Code != http.StatusFound {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusFound)
			}
			if got := rec.Header().Get("Location"); got != tt.want {
				t.Errorf("Location = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildMiddlewareStampsEveryResponse(t *testing.T) {
	original, originalCommit := version.Version, version.Commit
	t.Cleanup(func() { version.Version, version.Commit = original, originalCommit })
	version.Version, version.Commit = "1.2.3", "abc1234"

	// Denied and redirected requests must carry the header too: a tab whose
	// session lapsed during an upgrade is exactly the one that needs to reload.
	for _, status := range []int{http.StatusOK, http.StatusUnauthorized, http.StatusFound} {
		handler := buildMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
		}))

		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agents", nil))

		if got := rec.Header().Get(BuildHeader); got != "1.2.3@abc1234" {
			t.Errorf("status %d: %s = %q, want %q", status, BuildHeader, got, "1.2.3@abc1234")
		}
	}
}

func TestBuildMiddlewareOmitsEmptyCommit(t *testing.T) {
	original, originalCommit := version.Version, version.Commit
	t.Cleanup(func() { version.Version, version.Commit = original, originalCommit })
	version.Version, version.Commit = "dev", ""

	handler := buildMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if got := rec.Header().Get(BuildHeader); got != "dev" {
		t.Errorf("%s = %q, want %q", BuildHeader, got, "dev")
	}
}
