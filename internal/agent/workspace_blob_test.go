package agent

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/CherryHQ/stella/internal/blob"
	"github.com/CherryHQ/stella/internal/config"
)

func TestSaveAssetMirrorsToDefaultBlobStore(t *testing.T) {
	defer blob.ResetDefaultForTest()
	t.Setenv("STELLA_HOME", t.TempDir())
	config.ResetStellaHome()
	defer config.ResetStellaHome()

	remote, err := blob.NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := blob.SetDefault(remote); err != nil {
		t.Fatal(err)
	}
	assetsDir := filepath.Join(config.StellaHome(), "users", "u1", "data", "assets")
	if err := os.MkdirAll(assetsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	localPath, err := SaveAsset(assetsDir, "photo.png", []byte("image"))
	if err != nil {
		t.Fatalf("SaveAsset: %v", err)
	}
	key, err := blob.KeyForPath(config.StellaHome(), localPath)
	if err != nil {
		t.Fatal(err)
	}
	rc, err := remote.Open(context.Background(), key)
	if err != nil {
		t.Fatalf("remote Open(%q): %v", key, err)
	}
	remoteData, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil || string(remoteData) != "image" {
		t.Fatalf("remote data=%q err=%v", remoteData, err)
	}
	data, err := os.ReadFile(localPath)
	if err != nil || string(data) != "image" {
		t.Fatalf("local data=%q err=%v", data, err)
	}
}
