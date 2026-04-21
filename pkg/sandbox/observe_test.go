package sandbox

import (
	"strings"
	"testing"
)

func TestNewSessionID(t *testing.T) {
	a := NewSessionID()
	b := NewSessionID()
	if !strings.HasPrefix(a, "sandbox-") {
		t.Fatalf("expected sandbox- prefix, got %q", a)
	}
	if a == b {
		t.Fatal("successive IDs should be unique")
	}
}

func TestItoa(t *testing.T) {
	cases := []struct {
		in   uint64
		want string
	}{
		{0, "0"},
		{1, "1"},
		{42, "42"},
		{999, "999"},
		{1000000, "1000000"},
	}
	for _, c := range cases {
		got := itoa(c.in)
		if got != c.want {
			t.Fatalf("itoa(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestLogFunctions(t *testing.T) {
	p := Policy{
		Filesystem: FilesystemPolicy{WorkingDir: "/tmp/ws"},
		Network:    NetworkPolicy{Mode: NetworkDisabled},
	}
	// These should not panic
	LogSessionCreated("s1", "docker", p)
	LogSessionClosed("s1", "docker", "normal")
	LogUnsupportedBackend(p, []string{"docker"}, "no daemon")
	LogPolicyDenied("s1", "docker", "read", "/etc/passwd", "outside workspace")
	LogExceptionPath("e1", "runner", "read", "extra detail")
}
