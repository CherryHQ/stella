package pluginhost

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
)

type Catalog struct {
	defs map[string]Definition
}

func Discover(roots ...string) (*Catalog, error) {
	defs := make(map[string]Definition)

	for _, root := range roots {
		if root == "" {
			continue
		}
		if err := walkRoot(root, defs); err != nil {
			return nil, err
		}
	}

	return &Catalog{defs: defs}, nil
}

func walkRoot(root string, defs map[string]Definition) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || entry.Name() != ManifestFilename {
			return nil
		}

		def, err := LoadDefinition(path)
		if err != nil {
			return err
		}
		if existing, ok := defs[def.ID()]; ok {
			return fmt.Errorf("duplicate plugin definition %q: %s and %s", def.ID(), existing.ManifestPath, path)
		}
		defs[def.ID()] = def
		return nil
	})
}

func (c *Catalog) Get(id string) (Definition, bool) {
	if c == nil {
		return Definition{}, false
	}
	def, ok := c.defs[id]
	return def, ok
}

func (c *Catalog) List() []Definition {
	if c == nil {
		return nil
	}
	out := make([]Definition, 0, len(c.defs))
	for _, def := range c.defs {
		out = append(out, def)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Manifest.Kind == out[j].Manifest.Kind {
			return out[i].Manifest.Name < out[j].Manifest.Name
		}
		return out[i].Manifest.Kind < out[j].Manifest.Kind
	})
	return out
}
