package auth_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vaayne/anna/internal/auth"
)

func TestNewSessionID(t *testing.T) {
	id1 := auth.NewSessionID()
	id2 := auth.NewSessionID()

	if len(id1) != 64 {
		t.Errorf("session ID length = %d, want 64", len(id1))
	}
	if id1 == id2 {
		t.Error("two session IDs should not be equal")
	}
}

func TestSetAndGetSessionCookie(t *testing.T) {
	rr := httptest.NewRecorder()
	auth.SetSessionCookie(rr, "test-session-id", false)

	cookies := rr.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("no cookies set")
	}

	var found *http.Cookie
	for _, c := range cookies {
		if c.Name == auth.SessionCookieName {
			found = c
			break
		}
	}
	if found == nil {
		t.Fatal("session cookie not found")
	} else {
		if found.Value != "test-session-id" {
			t.Errorf("cookie value = %q, want %q", found.Value, "test-session-id")
		}
		if !found.HttpOnly {
			t.Error("cookie should be HttpOnly")
		}
		if found.SameSite != http.SameSiteLaxMode {
			t.Error("cookie should be SameSite=Lax")
		}
		if found.Secure {
			t.Error("cookie should not be Secure for non-secure")
		}
		if found.Path != "/" {
			t.Errorf("cookie path = %q, want %q", found.Path, "/")
		}
	}

	// Test Get.
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(found)
	id, err := auth.GetSessionCookie(req)
	if err != nil {
		t.Fatalf("GetSessionCookie: %v", err)
	}
	if id != "test-session-id" {
		t.Errorf("got %q, want %q", id, "test-session-id")
	}
}

func TestSetSessionCookieSecure(t *testing.T) {
	rr := httptest.NewRecorder()
	auth.SetSessionCookie(rr, "secure-id", true)

	for _, c := range rr.Result().Cookies() {
		if c.Name == auth.SessionCookieName {
			if !c.Secure {
				t.Error("cookie should be Secure")
			}
			return
		}
	}
	t.Fatal("session cookie not found")
}

func TestClearSessionCookie(t *testing.T) {
	rr := httptest.NewRecorder()
	auth.ClearSessionCookie(rr)

	for _, c := range rr.Result().Cookies() {
		if c.Name == auth.SessionCookieName {
			if c.MaxAge != -1 {
				t.Errorf("MaxAge = %d, want -1", c.MaxAge)
			}
			return
		}
	}
	t.Fatal("session cookie not found")
}

func TestGetSessionCookieMissing(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	_, err := auth.GetSessionCookie(req)
	if !errors.Is(err, auth.ErrNoSession) {
		t.Errorf("err = %v, want ErrNoSession", err)
	}
}

func TestGetSessionCookieEmpty(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: ""})
	_, err := auth.GetSessionCookie(req)
	if !errors.Is(err, auth.ErrNoSession) {
		t.Errorf("err = %v, want ErrNoSession", err)
	}
}
