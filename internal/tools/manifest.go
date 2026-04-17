package tools

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

const manifestFile = ".tools-manifest.json"

// Manifest tracks installed tool versions.
type Manifest struct {
	Tools map[string]InstalledTool `json:"tools"`
}

// InstalledTool records the installed version and platform of a tool.
type InstalledTool struct {
	Version  string `json:"version"`
	Platform string `json:"platform"`
}

// LoadManifest reads the manifest from binDir. If the file does not exist,
// an empty manifest is returned.
func LoadManifest(binDir string) (*Manifest, error) {
	p := filepath.Join(binDir, manifestFile)
	data, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &Manifest{Tools: make(map[string]InstalledTool)}, nil
		}
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	if m.Tools == nil {
		m.Tools = make(map[string]InstalledTool)
	}
	return &m, nil
}

// Save writes the manifest to binDir.
func (m *Manifest) Save(binDir string) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}
	p := filepath.Join(binDir, manifestFile)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}
	return nil
}

// IsInstalled reports whether the named tool is installed at the given version.
func (m *Manifest) IsInstalled(name, version string) bool {
	t, ok := m.Tools[name]
	return ok && t.Version == version
}
