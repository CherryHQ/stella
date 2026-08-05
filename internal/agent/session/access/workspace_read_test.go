package access

import (
	"bytes"
	"errors"
	"os"
	"testing"

	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
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

func TestWorkspaceAssetAlias(t *testing.T) {
	valid := "$" + pkgsandbox.EnvStellaAssetsDir + "/202608/file.png"
	got, isAlias, err := workspaceAssetAlias(valid)
	if err != nil || !isAlias || got != "assets/202608/file.png" {
		t.Fatalf("workspaceAssetAlias(%q) = %q, %t, %v", valid, got, isAlias, err)
	}

	for _, input := range []string{
		"$" + pkgsandbox.EnvStellaAssetsDir,
		"$" + pkgsandbox.EnvStellaAssetsDir + "/../secret.txt",
		"$" + pkgsandbox.EnvStellaAssetsDir + "/assets/./file.png",
		"$HOME/file.png",
		"${STELLA_ASSETS_DIR",
	} {
		t.Run(input, func(t *testing.T) {
			_, isAlias, err := workspaceAssetAlias(input)
			if !isAlias || !errors.Is(err, ErrInvalid) {
				t.Fatalf("workspaceAssetAlias(%q) = isAlias=%t, err=%v; want invalid alias", input, isAlias, err)
			}
		})
	}
}
