package access

import (
	"bytes"
	"errors"
	"os"
	"testing"
)

func TestReadRootFileEnforcesLimitDuringRead(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/large.bin", bytes.Repeat([]byte("x"), 17), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()

	if _, err := readRootFile(root, "large.bin", 16); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("read error = %v, want ErrTooLarge", err)
	}
	got, err := readRootFile(root, "large.bin", 17)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 17 {
		t.Fatalf("read bytes = %d, want 17", len(got))
	}
}
