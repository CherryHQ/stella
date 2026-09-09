package bridge

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	sandboxpkg "github.com/CherryHQ/stella/pkg/sandbox"
)

func TestLoadBinding(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name+".json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("u1", `{"socket":"/tmp/b.sock","nonce":"abc","workdir":"/app","home":"/root"}`)
	write("bad-workdir", `{"socket":"/tmp/b.sock","nonce":"abc","workdir":"app"}`)
	write("no-nonce", `{"socket":"/tmp/b.sock","workdir":"/app"}`)

	b, err := LoadBinding(dir, "u1")
	if err != nil {
		t.Fatalf("valid binding: %v", err)
	}
	if b.Socket != "/tmp/b.sock" || b.Nonce != "abc" || b.WorkDir != "/app" || b.Home != "/root" {
		t.Fatalf("unexpected binding %+v", b)
	}

	if _, err := LoadBinding(dir, "missing"); !errors.Is(err, ErrNoBinding) {
		t.Fatalf("missing binding: want ErrNoBinding, got %v", err)
	}
	if _, err := LoadBinding(dir, "bad-workdir"); err == nil {
		t.Fatal("relative workdir must be rejected")
	}
	if _, err := LoadBinding(dir, "no-nonce"); err == nil {
		t.Fatal("binding without nonce must be rejected")
	}
	if _, err := LoadBinding(dir, "../u1"); err == nil {
		t.Fatal("path traversal in principal id must be rejected")
	}
	if _, err := LoadBinding("", "u1"); err == nil {
		t.Fatal("empty binding dir must be rejected")
	}
}

func TestBindingDeadlineSurvivesSessionWrapper(t *testing.T) {
	const deadline = "2030-01-02T03:04:05.123456Z"
	dir := t.TempDir()
	body := `{"socket":"/tmp/b.sock","nonce":"abc","workdir":"/app","deadline":"` + deadline + `"}`
	if err := os.WriteFile(filepath.Join(dir, "u.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := LoadBinding(dir, "u")
	if err != nil {
		t.Fatal(err)
	}
	wrapped := sandboxpkg.NewResilientSession(&session{deadline: b.Deadline}, nil)
	got, ok := wrapped.TurnDeadline()
	if !ok || got.Format(time.RFC3339Nano) != deadline {
		t.Fatalf("deadline=%v present=%v", got, ok)
	}
}
