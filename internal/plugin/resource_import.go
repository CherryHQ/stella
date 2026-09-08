package plugin

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"

	"github.com/CherryHQ/stella/internal/plugin/agentpackage"
)

func importResourceFiles(source string) (map[string]ResourceFile, string, error) {
	staging, err := os.MkdirTemp("", "stella-plugin-import-")
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = os.RemoveAll(staging) }()
	published, err := agentpackage.PublishDirectory(source, staging)
	if err != nil {
		return nil, "", fmt.Errorf("plugin: import package: %w", err)
	}
	if published.Package == nil {
		return nil, "", ErrInvalidDefinition
	}
	root, err := os.OpenRoot(published.Root)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = root.Close() }()
	files := make(map[string]ResourceFile)
	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("plugin: imported file %q is not regular", name)
		}
		data, err := fs.ReadFile(root.FS(), name)
		if err != nil {
			return err
		}
		files[path.Clean(name)] = ResourceFile{Data: data, Mode: info.Mode().Perm()}
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	return files, published.Package.Manifest.Name, nil
}

func rewriteManifestName(files map[string]ResourceFile, name string) error {
	manifest, ok := files["plugin.json"]
	if !ok {
		return fmt.Errorf("plugin: %w: plugin.json is required", ErrInvalidDefinition)
	}
	var object map[string]any
	if err := json.Unmarshal(manifest.Data, &object); err != nil || object == nil {
		return fmt.Errorf("plugin: %w: invalid plugin.json", ErrInvalidDefinition)
	}
	object["name"] = name
	data, err := json.Marshal(object)
	if err != nil {
		return err
	}
	manifest.Data = data
	files["plugin.json"] = manifest
	return nil
}
