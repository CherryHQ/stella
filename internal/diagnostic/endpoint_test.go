package diagnostic

import (
	"strings"
	"testing"
)

func TestEndpointRemovesSecrets(t *testing.T) {
	const raw = "https://user:canary-userinfo@collector.example:4318/v1/traces?api_key=canary-query#canary-fragment"
	got := Endpoint(raw)
	want := "https://collector.example:4318/v1/traces"
	if got != want {
		t.Fatalf("Endpoint(%q) = %q, want %q", raw, got, want)
	}
	for _, secret := range []string{"canary-userinfo", "canary-query", "canary-fragment"} {
		if strings.Contains(got, secret) {
			t.Fatalf("Endpoint leaked %q in %q", secret, got)
		}
	}
}

func TestEndpointDoesNotEchoMalformedValue(t *testing.T) {
	const raw = "https://canary-userinfo@collector.example/%zz?api_key=canary-query#canary-fragment"
	if got := Endpoint(raw); got != invalidEndpoint {
		t.Fatalf("Endpoint(%q) = %q, want %q", raw, got, invalidEndpoint)
	}
}
