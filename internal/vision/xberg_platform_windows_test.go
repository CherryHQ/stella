//go:build windows

package vision

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/CherryHQ/stella/internal/config"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

func TestXbergFallbackWindowsFailsBeforeStagingOrCommand(t *testing.T) {
	temp := t.TempDir()
	t.Setenv("TMP", temp)
	t.Setenv("TEMP", temp)
	stellaHome := t.TempDir()
	t.Setenv("STELLA_HOME", stellaHome)
	config.ResetStellaHome()
	t.Cleanup(config.ResetStellaHome)

	// An invalid shim is a command trap: reaching process startup would not
	// produce the stable unsupported-platform error.
	shim := filepath.Join(pkgsandbox.MiseShimsDir(stellaHome), "xberg.exe")
	if err := os.MkdirAll(filepath.Dir(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shim, []byte("not an executable"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadDir(temp)
	if err != nil {
		t.Fatal(err)
	}
	_, err = extractBytesWithXberg(context.Background(), []byte("image"), "image/png")
	if !errors.Is(err, errXbergUnsupportedPlatform) {
		t.Fatalf("extractBytesWithXberg error = %v, want unsupported platform", err)
	}
	after, err := os.ReadDir(temp)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("Windows fallback created staging entries: before=%d after=%d", len(before), len(after))
	}
}
