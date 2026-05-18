package resources

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"sync"
)

// Registry is the read-only catalog of builtin resources, keyed by Kind and ID.
type Registry struct {
	byKind map[Kind]map[string]Resource
}

var (
	defaultOnce sync.Once
	defaultReg  *Registry
	defaultErr  error
)

// Default returns the process-wide registry, loaded lazily from the embedded FS.
func Default() (*Registry, error) {
	defaultOnce.Do(func() {
		defaultReg, defaultErr = Load(fsys)
	})
	return defaultReg, defaultErr
}

// Load walks sourceFS and parses every supported resource kind it finds.
// sourceFS must have subdirectories matching Kind.subdir() for each kind to load.
// Missing subdirectories are silently skipped (useful for tests with partial fixtures).
func Load(sourceFS fs.FS) (*Registry, error) {
	r := &Registry{byKind: make(map[Kind]map[string]Resource, len(AllKinds()))}
	for _, kind := range AllKinds() {
		r.byKind[kind] = map[string]Resource{}
		sub := kind.subdir()
		if sub == "" {
			continue
		}
		subFS, err := fs.Sub(sourceFS, sub)
		if err != nil {
			continue
		}
		if err := loadKind(r, kind, subFS); err != nil {
			return nil, fmt.Errorf("load %s: %w", kind, err)
		}
	}
	return r, nil
}

// loadKind discovers resources of a single kind under subFS.
// Skills are multi-file directories (id = dir name, main = SKILL.md).
// Souls/delegates/templates are single files (id = basename without .md).
func loadKind(r *Registry, kind Kind, subFS fs.FS) error {
	entries, err := fs.ReadDir(subFS, ".")
	if err != nil {
		// Missing kind dir is fine — treat as empty (useful for test fixtures).
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read %s: %w", kind.subdir(), err)
	}

	if kind == KindSkill {
		roots, err := listSkillRoots(subFS)
		if err != nil {
			return fmt.Errorf("discover skills: %w", err)
		}
		for _, root := range roots {
			raw, err := fs.ReadFile(subFS, path.Join(root.Path, "SKILL.md"))
			if err != nil {
				return fmt.Errorf("read %s/SKILL.md: %w", root.Path, err)
			}
			res, err := parseResource(kind, root.Leaf, string(raw))
			if err != nil {
				return fmt.Errorf("skill %s: %w", root.Path, err)
			}
			r.byKind[kind][res.ID] = res
		}
		return nil
	}

	for _, entry := range entries {

		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".md") {
			continue
		}
		id := strings.TrimSuffix(name, ".md")
		raw, err := fs.ReadFile(subFS, name)
		if err != nil {
			return fmt.Errorf("read %s/%s: %w", kind.subdir(), name, err)
		}
		res, err := parseResource(kind, id, string(raw))
		if err != nil {
			return fmt.Errorf("%s %s: %w", kind, id, err)
		}
		r.byKind[kind][res.ID] = res
	}
	return nil
}

// List returns all resources of the given kind, sorted by ID for determinism.
func (r *Registry) List(kind Kind) []Resource {
	m := r.byKind[kind]
	out := make([]Resource, 0, len(m))
	for _, res := range m {
		out = append(out, res)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Get fetches a single resource by kind and ID.
func (r *Registry) Get(kind Kind, id string) (Resource, bool) {
	res, ok := r.byKind[kind][id]
	return res, ok
}

// Kinds returns the set of kinds that have at least one loaded resource.
func (r *Registry) Kinds() []Kind {
	out := make([]Kind, 0, len(r.byKind))
	for _, k := range AllKinds() {
		if len(r.byKind[k]) > 0 {
			out = append(out, k)
		}
	}
	return out
}
