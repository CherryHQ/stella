package toolinstall

import (
	"os"
	"path/filepath"
	"testing"
)

func assertNoPrewarmPublication(t *testing.T, home string) {
	t.Helper()
	for _, name := range []string{"plugin-manifest-state.json", ".mise-tools/configs/_builtin.toml", ".mise-tools/public"} {
		if _, err := os.Stat(filepath.Join(home, name)); !os.IsNotExist(err) {
			t.Fatalf("prewarm published %s: %v", name, err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(home, ".mise-private"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("private install inputs survived: %v, %v", entries, err)
	}
}

func TestWarmEmptyToolsCreatesNoState(t *testing.T) {
	home := t.TempDir()
	if err := Warm(t.Context(), home, nil); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(home)
	if err != nil || len(entries) != 0 {
		t.Fatalf("empty prewarm created state: %v, %v", entries, err)
	}
}
