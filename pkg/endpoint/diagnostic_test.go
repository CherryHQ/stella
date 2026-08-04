package endpoint

import (
	"strings"
	"testing"
)

func TestForDiagnosticRemovesEndpointSecrets(t *testing.T) {
	const raw = "https://user:canary-userinfo@collector.example:4318/v1/traces?api_key=canary-query#canary-fragment"
	got := ForDiagnostic(raw)
	want := "https://collector.example:4318/v1/traces"
	if got != want {
		t.Fatalf("ForDiagnostic(%q) = %q, want %q", raw, got, want)
	}
	for _, secret := range []string{"canary-userinfo", "canary-query", "canary-fragment"} {
		if strings.Contains(got, secret) {
			t.Fatalf("ForDiagnostic leaked %q in %q", secret, got)
		}
	}
}

func TestForDiagnosticDoesNotEchoMalformedEndpoint(t *testing.T) {
	const raw = "https://canary-userinfo@collector.example/%zz?api_key=canary-query#canary-fragment"
	if got := ForDiagnostic(raw); got != invalid {
		t.Fatalf("ForDiagnostic(%q) = %q, want %q", raw, got, invalid)
	}
}
