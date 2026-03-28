package pluginhost

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/vaayne/anna/internal/pluginapi"
)

const ManifestFilename = "plugin.json"
const BuiltinEntrypoint = "@anna"

type Definition struct {
	Manifest     pluginapi.Manifest
	ManifestPath string
	RootDir      string
}

func LoadDefinition(path string) (Definition, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Definition{}, fmt.Errorf("read manifest: %w", err)
	}

	var manifest pluginapi.Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return Definition{}, fmt.Errorf("parse manifest: %w", err)
	}

	def := Definition{
		Manifest:     manifest,
		ManifestPath: path,
		RootDir:      filepath.Dir(path),
	}
	if err := def.Validate(); err != nil {
		return Definition{}, err
	}
	return def, nil
}

func (d Definition) Validate() error {
	switch {
	case d.Manifest.Name == "":
		return fmt.Errorf("manifest %s: name is required", d.ManifestPath)
	case d.Manifest.Version == "":
		return fmt.Errorf("manifest %s: version is required", d.ManifestPath)
	case d.Manifest.Kind == "":
		return fmt.Errorf("manifest %s: kind is required", d.ManifestPath)
	case d.Manifest.ProtocolVersion == "":
		return fmt.Errorf("manifest %s: protocol_version is required", d.ManifestPath)
	case d.Manifest.ProtocolVersion != pluginapi.ProtocolVersion:
		return fmt.Errorf("manifest %s: unsupported protocol_version %q", d.ManifestPath, d.Manifest.ProtocolVersion)
	case d.Manifest.Entrypoint == "":
		return fmt.Errorf("manifest %s: entrypoint is required", d.ManifestPath)
	}

	entrypoint := d.Entrypoint()
	if entrypoint != BuiltinEntrypoint {
		info, err := os.Stat(entrypoint)
		if err != nil {
			return fmt.Errorf("manifest %s: entrypoint %q: %w", d.ManifestPath, entrypoint, err)
		}
		if info.IsDir() {
			return fmt.Errorf("manifest %s: entrypoint %q is a directory", d.ManifestPath, entrypoint)
		}
	}

	return nil
}

func (d Definition) Entrypoint() string {
	if filepath.IsAbs(d.Manifest.Entrypoint) {
		return d.Manifest.Entrypoint
	}
	return filepath.Join(d.RootDir, d.Manifest.Entrypoint)
}

func (d Definition) ID() string {
	return string(d.Manifest.Kind) + "/" + d.Manifest.Name
}
