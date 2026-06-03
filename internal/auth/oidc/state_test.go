package oidc

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

const testVaultKey = "test-vault-key-for-oidc-state"

// cookieRequest builds an *http.Request carrying the cookies from a recorder response.
func cookieRequest(t *testing.T, w *httptest.ResponseRecorder) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	for _, c := range w.Result().Cookies() {
		req.AddCookie(c)
	}
	return req
}

func TestStateManager_GenerateAndValidate(t *testing.T) {
	mgr, err := NewStateManager(testVaultKey)
	if err != nil {
		t.Fatal(err)
	}

	payload, err := mgr.Generate()
	if err != nil {
		t.Fatal(err)
	}
	payload.ProviderName = "acme"
	if payload.State == "" {
		t.Error("state is empty")
	}
	if payload.CodeVerifier == "" {
		t.Error("code verifier is empty")
	}

	w := httptest.NewRecorder()
	if err := mgr.SetCookie(w, payload, false); err != nil {
		t.Fatal(err)
	}

	req := cookieRequest(t, w)
	got, err := mgr.ValidateAndClear(httptest.NewRecorder(), req, payload.State)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != payload.State {
		t.Errorf("state mismatch: got %q want %q", got.State, payload.State)
	}
	if got.CodeVerifier != payload.CodeVerifier {
		t.Errorf("code verifier mismatch: got %q want %q", got.CodeVerifier, payload.CodeVerifier)
	}
	if got.ProviderName != payload.ProviderName {
		t.Errorf("provider mismatch: got %q want %q", got.ProviderName, payload.ProviderName)
	}
}

func TestStateManager_StateMismatch(t *testing.T) {
	mgr, err := NewStateManager(testVaultKey)
	if err != nil {
		t.Fatal(err)
	}

	payload, _ := mgr.Generate()
	w := httptest.NewRecorder()
	_ = mgr.SetCookie(w, payload, false)

	req := cookieRequest(t, w)
	_, err = mgr.ValidateAndClear(httptest.NewRecorder(), req, "wrong-state")
	if err == nil {
		t.Error("expected error for state mismatch")
	}
}

func TestStateManager_Expired(t *testing.T) {
	mgr, err := NewStateManager(testVaultKey)
	if err != nil {
		t.Fatal(err)
	}

	payload := StateCookiePayload{
		State:        "test-state",
		CodeVerifier: "test-verifier",
		CreatedAt:    time.Now().Add(-700 * time.Second), // past the 600s limit
	}

	w := httptest.NewRecorder()
	if err := mgr.SetCookie(w, payload, false); err != nil {
		t.Fatal(err)
	}

	req := cookieRequest(t, w)
	_, err = mgr.ValidateAndClear(httptest.NewRecorder(), req, payload.State)
	if err == nil {
		t.Error("expected error for expired state")
	}
}

func TestStateManager_TamperedSignature(t *testing.T) {
	mgr, err := NewStateManager(testVaultKey)
	if err != nil {
		t.Fatal(err)
	}

	payload, _ := mgr.Generate()
	w := httptest.NewRecorder()
	_ = mgr.SetCookie(w, payload, false)

	// Tamper: flip the last byte of the cookie value.
	req := cookieRequest(t, w)
	for _, c := range req.Cookies() {
		if c.Name == stateCookieName && len(c.Value) > 0 {
			v := []byte(c.Value)
			v[len(v)-1] ^= 0xFF
			req.Header.Set("Cookie", c.Name+"="+string(v))
		}
	}

	_, err = mgr.ValidateAndClear(httptest.NewRecorder(), req, payload.State)
	if err == nil {
		t.Error("expected error for tampered signature")
	}
}

func TestStateManager_WrongKey(t *testing.T) {
	mgr1, _ := NewStateManager("key-one")
	mgr2, _ := NewStateManager("key-two")

	payload, _ := mgr1.Generate()
	w := httptest.NewRecorder()
	_ = mgr1.SetCookie(w, payload, false)

	req := cookieRequest(t, w)
	_, err := mgr2.ValidateAndClear(httptest.NewRecorder(), req, payload.State)
	if err == nil {
		t.Error("expected error when validating with wrong key")
	}
}

func TestPKCEChallenge(t *testing.T) {
	verifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	// RFC 7636 example: SHA256 of verifier, base64url-encoded
	challenge, method, err := pkceChallenge(verifier)
	if err != nil {
		t.Fatal(err)
	}
	if method != "S256" {
		t.Errorf("method = %q, want S256", method)
	}
	if challenge == "" {
		t.Error("challenge is empty")
	}
	// Same verifier should produce same challenge.
	c2, _, _ := pkceChallenge(verifier)
	if challenge != c2 {
		t.Error("pkceChallenge is not deterministic")
	}
}
