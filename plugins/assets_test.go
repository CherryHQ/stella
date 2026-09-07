package plugins

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestBuiltinSkillsHaveOneOwnedEmbeddedSource(t *testing.T) {
	owners := map[string]string{"stella": "stella", "xberg": "xberg", "lark-cli": "lark-cli"}
	for _, asset := range BuiltinSkillAssets() {
		if asset.OwnerPluginID == "" || asset.SourceRoot == "" || asset.LogicalRoot == "" {
			t.Fatalf("skill %q has incomplete ownership: %#v", asset.Name, asset)
		}
		if want, ok := owners[asset.Name]; ok {
			if asset.OwnerPluginID != want {
				t.Errorf("skill %s owner = %s, want %s", asset.Name, asset.OwnerPluginID, want)
			}
			delete(owners, asset.Name)
		}
		disk := os.DirFS(filepath.FromSlash(asset.SourceRoot))
		if err := fs.WalkDir(disk, ".", func(name string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return err
			}
			want, err := fs.ReadFile(disk, name)
			if err != nil {
				return err
			}
			got, err := ReadBuiltinSkillFile(asset.SourceRoot, name)
			if err != nil {
				return err
			}
			if !bytes.Equal(got, want) {
				t.Errorf("embedded skill %s/%s differs from its source", asset.Name, name)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if len(owners) != 0 {
		t.Fatalf("missing builtin skills: %v", owners)
	}
}
