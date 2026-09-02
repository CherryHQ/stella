package webfetch

import (
	"net/http"
	"net/url"
	"testing"
	"time"
)

func TestValidatePublicURLRejectsUnsafeTargets(t *testing.T) {
	for _, raw := range []string{
		"file:///etc/passwd",
		"http://localhost/",
		"http://127.0.0.1/",
		"http://[::1]/",
		"http://10.0.0.1/",
		"http://100.64.0.1/",
		"http://192.0.0.9/",
		"http://198.18.0.1/",
		"http://[64:ff9b::1]/",
		"http://[2002:0a00:0001::1]/",
		"https://example.com:8443/",
		"https://user:password@example.com/",
		"https://example.com/?access_token=secret",
	} {
		t.Run(raw, func(t *testing.T) {
			u, err := url.Parse(raw)
			if err != nil {
				t.Fatal(err)
			}
			if err := validatePublicURL(u); err == nil {
				t.Fatalf("validatePublicURL(%q) succeeded", raw)
			}
		})
	}
}

func TestValidatePublicURLAllowsOrdinaryPublicURL(t *testing.T) {
	u, err := url.Parse("https://www.example.com/articles?id=42#section")
	if err != nil {
		t.Fatal(err)
	}
	if err := validatePublicURL(u); err != nil {
		t.Fatalf("validatePublicURL() error = %v", err)
	}
}

func TestPublicClientIgnoresEnvironmentProxy(t *testing.T) {
	client := newPublicClient(time.Second)
	transport, ok := client.Transport.(publicTransport)
	if !ok {
		t.Fatalf("transport = %T, want publicTransport", client.Transport)
	}
	base, ok := transport.base.(*http.Transport)
	if !ok {
		t.Fatalf("base transport = %T, want *http.Transport", transport.base)
	}
	if base.Proxy != nil {
		t.Fatal("public client inherits environment proxy, which bypasses target address validation")
	}
}

func TestPublicClientRevalidatesRedirect(t *testing.T) {
	client := newPublicClient(time.Second)
	first, err := http.NewRequest(http.MethodGet, "https://example.com/", nil)
	if err != nil {
		t.Fatal(err)
	}
	target, err := http.NewRequest(http.MethodGet, "http://127.0.0.1/", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.CheckRedirect(target, []*http.Request{first}); err == nil {
		t.Fatal("CheckRedirect() accepted a loopback redirect")
	}
}

func TestPublicClientLimitsRedirects(t *testing.T) {
	client := newPublicClient(time.Second)
	via := make([]*http.Request, maxRedirects+1)
	for i := range via {
		req, err := http.NewRequest(http.MethodGet, "https://example.com/", nil)
		if err != nil {
			t.Fatal(err)
		}
		via[i] = req
	}
	target, err := http.NewRequest(http.MethodGet, "https://www.example.com/", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.CheckRedirect(target, via); err == nil {
		t.Fatal("CheckRedirect() accepted too many redirects")
	}
}
