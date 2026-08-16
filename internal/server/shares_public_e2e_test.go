package server_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/internal/auth"
	sharepkg "github.com/CherryHQ/stella/internal/share"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestPublicShareAnonymousFullChain(t *testing.T) {
	env := setupAdmin(t)
	ctx := context.Background()
	q := sqlc.New(env.db)

	foreign, foreignSession := createTestUserWithToken(t, env.authStore, env.oidcStore, "share-foreign", auth.RoleUser)
	expiresAt := time.Now().UTC().Add(time.Hour).Truncate(time.Second)

	createShare := func(t *testing.T, userID, token, title, mediaType, content string, expires time.Time) sqlc.Share {
		t.Helper()
		row, err := q.CreateShare(ctx, sqlc.CreateShareParams{
			ID:        uuid.NewString(),
			TokenHash: sharepkg.TokenHash(token),
			UserID:    userID,
			Title:     title,
			MediaType: mediaType,
			Content:   []byte(content),
			ExpiresAt: pgtype.Timestamptz{Time: expires, Valid: true},
		})
		if err != nil {
			t.Fatalf("CreateShare(%q): %v", title, err)
		}
		if row.TokenHash != sharepkg.TokenHash(token) || row.TokenHash == token {
			t.Fatalf("stored token = %q, want only SHA-256 hash", row.TokenHash)
		}
		return row
	}

	const (
		liveToken    = "full-chain-live-token"
		liveTitle    = "owner-artifact.html"
		liveType     = "text/html; charset=utf-8"
		liveContent  = "<html><body>owner artifact bytes</body></html>"
		foreignToken = "full-chain-foreign-token"
		foreignTitle = "foreign-artifact.txt"
		foreignBody  = "foreign tenant secret bytes"
	)
	createShare(t, env.adminUser.ID, liveToken, liveTitle, liveType, liveContent, expiresAt)
	createShare(t, foreign.ID, foreignToken, foreignTitle, "text/plain; charset=utf-8", foreignBody, expiresAt)

	publicPath := "/api/shares/public/" + liveToken

	t.Run("live anonymous GET returns artifact bytes and metadata", func(t *testing.T) {
		rr := doUnauthRequest(t, env.srv, http.MethodGet, publicPath, nil)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
		}
		if got := rr.Body.String(); got != liveContent {
			t.Fatalf("body = %q, want %q", got, liveContent)
		}
		assertShareHeaders(t, rr.Header(), liveTitle, liveType, expiresAt)
	})

	t.Run("live anonymous HEAD returns metadata and no body", func(t *testing.T) {
		rr := doUnauthRequest(t, env.srv, http.MethodHead, publicPath, nil)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
		}
		if rr.Body.Len() != 0 {
			t.Fatalf("HEAD body = %q, want empty", rr.Body.String())
		}
		if got := rr.Header().Get("Content-Length"); got != "46" {
			t.Fatalf("Content-Length = %q, want 46", got)
		}
		assertShareHeaders(t, rr.Header(), liveTitle, liveType, expiresAt)
	})

	t.Run("other methods remain authenticated", func(t *testing.T) {
		for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
			t.Run(method, func(t *testing.T) {
				rr := doUnauthRequest(t, env.srv, method, publicPath, nil)
				if rr.Code != http.StatusUnauthorized {
					t.Fatalf("status = %d, want 401 (body: %s)", rr.Code, rr.Body.String())
				}
				if got := parseResponse(t, rr).Error; got != "authentication required" {
					t.Fatalf("error = %q, want authentication required", got)
				}
			})
		}
	})

	t.Run("unrelated API route remains authenticated", func(t *testing.T) {
		rr := doUnauthRequest(t, env.srv, http.MethodGet, "/api/shares", nil)
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401 (body: %s)", rr.Code, rr.Body.String())
		}
	})

	t.Run("missing expired and revoked tokens are opaque 404 for GET and HEAD", func(t *testing.T) {
		const expiredToken = "full-chain-expired-token"
		createShare(t, env.adminUser.ID, expiredToken, "expired.txt", "text/plain", "expired", time.Now().UTC().Add(-time.Hour))

		const revokedToken = "full-chain-revoked-token"
		revoked := createShare(t, env.adminUser.ID, revokedToken, "revoked.txt", "text/plain", "revoked", expiresAt)
		rows, err := q.DeleteShareByUser(ctx, sqlc.DeleteShareByUserParams{ID: revoked.ID, UserID: env.adminUser.ID})
		if err != nil || rows != 1 {
			t.Fatalf("DeleteShareByUser rows=%d err=%v, want one revoked row", rows, err)
		}

		for name, token := range map[string]string{
			"missing": "full-chain-missing-token",
			"expired": expiredToken,
			"revoked": revokedToken,
		} {
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				t.Run(name+"_"+method, func(t *testing.T) {
					rr := doUnauthRequest(t, env.srv, method, "/api/shares/public/"+token, nil)
					if rr.Code != http.StatusNotFound {
						t.Fatalf("status = %d, want 404 (body: %s)", rr.Code, rr.Body.String())
					}
					if got := parseResponse(t, rr).Error; got != "share not found" {
						t.Fatalf("error = %q, want opaque share not found", got)
					}
				})
			}
		}
	})

	t.Run("tokens do not cross tenant rows or leak foreign metadata", func(t *testing.T) {
		owner := doUnauthRequest(t, env.srv, http.MethodGet, publicPath, nil)
		if owner.Code != http.StatusOK {
			t.Fatalf("owner status = %d, want 200", owner.Code)
		}
		if strings.Contains(owner.Body.String(), foreignBody) || owner.Header().Get("X-Share-Title") == foreignTitle {
			t.Fatalf("owner response leaked foreign share: headers=%v body=%q", owner.Header(), owner.Body.String())
		}

		other := doRequestWithSession(t, env.srv, foreignSession, http.MethodGet, publicPath, nil)
		if other.Code != http.StatusOK {
			t.Fatalf("foreign-session owner-token status=%d body=%q, want public 200", other.Code, other.Body.String())
		}
		if other.Body.String() != liveContent || other.Header().Get("X-Share-Title") != liveTitle {
			t.Fatalf("foreign-session owner-token response headers=%v body=%q, want exact owner share only", other.Header(), other.Body.String())
		}
		if strings.Contains(other.Body.String(), foreignBody) || other.Header().Get("X-Share-Title") == foreignTitle {
			t.Fatalf("foreign-session owner-token response leaked caller tenant share: headers=%v body=%q", other.Header(), other.Body.String())
		}

		foreignResponse := doUnauthRequest(t, env.srv, http.MethodGet, "/api/shares/public/"+foreignToken, nil)
		if foreignResponse.Code != http.StatusOK || foreignResponse.Body.String() != foreignBody {
			t.Fatalf("foreign response status=%d body=%q, want its own bytes", foreignResponse.Code, foreignResponse.Body.String())
		}
		if got := foreignResponse.Header().Get("X-Share-Title"); got != foreignTitle {
			t.Fatalf("foreign title = %q, want %q", got, foreignTitle)
		}
		if strings.Contains(foreignResponse.Body.String(), liveContent) || foreignResponse.Header().Get("X-Share-Title") == liveTitle {
			t.Fatalf("foreign response leaked owner share: headers=%v body=%q", foreignResponse.Header(), foreignResponse.Body.String())
		}
	})
}

func assertShareHeaders(t *testing.T, header http.Header, title, mediaType string, expiresAt time.Time) {
	t.Helper()
	for name, want := range map[string]string{
		"Content-Type":       mediaType,
		"X-Share-Title":      title,
		"X-Share-Media-Type": mediaType,
		"X-Share-Expires-At": expiresAt.Format(time.RFC3339),
		"Cache-Control":      "private, max-age=300",
	} {
		if got := header.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}
