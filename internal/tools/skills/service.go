package skills

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

// Service provides unified skill resolution across all 4 levels
// (project, user, agent, system). Both the CLI tool and HTTP server
// use this to avoid duplicating the merge/resolve logic.
type Service struct {
	store      pkgplugins.SkillStore
	stellaHome string
}

func NewService(store pkgplugins.SkillStore, stellaHome string) *Service {
	return &Service{store: store, stellaHome: stellaHome}
}

// ResolvedSkill is a skill with its filesystem directory (if applicable).
type ResolvedSkill struct {
	pkgplugins.Skill
	Dir string // absolute path on disk; empty for DB-only skills without disk sync
}

// ListMerged returns all visible skills across project, DB, and system levels,
// deduplicated by name with priority: project > DB (user/agent) > system.
// It uses the store's default List query which filters deprecated/disabled skills.
func (s *Service) ListMerged(ctx context.Context, vc pkgplugins.SkillViewContext, projectRoot string) ([]ResolvedSkill, error) {
	var dbSkills []pkgplugins.Skill
	if s.store != nil {
		var err error
		dbSkills, err = s.store.List(ctx, vc)
		if err != nil {
			return nil, fmt.Errorf("list db skills: %w", err)
		}
	}
	return s.mergeSkills(dbSkills, projectRoot), nil
}

// ListMergedWithDB merges the given DB skills with FS skills.
// Use this when the caller needs a different DB query (e.g. including disabled skills).
func (s *Service) ListMergedWithDB(dbSkills []pkgplugins.Skill, projectRoot string) []ResolvedSkill {
	return s.mergeSkills(dbSkills, projectRoot)
}

func (s *Service) mergeSkills(dbSkills []pkgplugins.Skill, projectRoot string) []ResolvedSkill {
	projSkills, projDirs, _ := ListProjectSkills(projectRoot)
	sysSkills, sysDirs, _ := ListSystemSkills(s.stellaHome)

	seen := make(map[string]bool, len(projSkills)+len(dbSkills)+len(sysSkills))
	out := make([]ResolvedSkill, 0, len(projSkills)+len(dbSkills)+len(sysSkills))

	for _, sk := range projSkills {
		seen[sk.Name] = true
		out = append(out, ResolvedSkill{Skill: sk, Dir: projDirs[sk.Name]})
	}
	for _, sk := range dbSkills {
		if seen[sk.Name] {
			continue
		}
		seen[sk.Name] = true
		out = append(out, ResolvedSkill{Skill: sk})
	}
	for _, sk := range sysSkills {
		if seen[sk.Name] {
			continue
		}
		out = append(out, ResolvedSkill{Skill: sk, Dir: sysDirs[sk.Name]})
	}
	return out
}

// Resolve finds a skill by name across all 4 levels.
// Priority: project > system > DB (user > agent > system in DB).
// FS skills are checked first so they always shadow DB skills of the same name.
func (s *Service) Resolve(ctx context.Context, name string, vc pkgplugins.SkillViewContext, projectRoot string) (*ResolvedSkill, error) {
	for _, lookup := range []struct {
		scope string
		root  string
	}{
		{"project", projectRoot},
		{"system", s.stellaHome},
	} {
		if lookup.root == "" {
			continue
		}
		skills, dirs, err := listFSSkills(lookup.root, lookup.scope)
		if err != nil {
			continue
		}
		for _, sk := range skills {
			if sk.Name == name {
				return &ResolvedSkill{Skill: sk, Dir: dirs[name]}, nil
			}
		}
	}

	if s.store == nil {
		return nil, nil
	}
	sk, err := s.store.Resolve(ctx, name, vc)
	if err != nil {
		return nil, err
	}
	if sk == nil {
		return nil, nil
	}
	return &ResolvedSkill{Skill: *sk}, nil
}

// LoadFile loads a file from a skill resolved by name.
// Returns the file content and the skill's directory path (if on disk).
func (s *Service) LoadFile(ctx context.Context, name, path string, vc pkgplugins.SkillViewContext, projectRoot string) (content string, skillDir string, err error) {
	if path == "" {
		path = pkgplugins.SkillMainFile
	}

	rs, err := s.Resolve(ctx, name, vc, projectRoot)
	if err != nil {
		return "", "", fmt.Errorf("resolve skill %q: %w", name, err)
	}
	if rs == nil {
		return "", "", fmt.Errorf("skill %q not found", name)
	}

	if rs.Dir != "" {
		data, err := loadProjectSkillFile(rs.Dir, path)
		if err != nil {
			return "", "", fmt.Errorf("load %s skill %q file %q: %w", rs.Scope, name, path, err)
		}
		return data, rs.Dir, nil
	}

	if s.store == nil {
		return "", "", fmt.Errorf("skill %q has no directory and store is unavailable", name)
	}
	data, err := s.store.LoadFile(ctx, rs.ID, path)
	if err != nil {
		return "", "", fmt.Errorf("load skill %q file %q: %w", name, path, err)
	}
	return data, "", nil
}

// ListFiles returns file paths for a resolved skill.
func (s *Service) ListFiles(ctx context.Context, name string, vc pkgplugins.SkillViewContext, projectRoot string) ([]string, string, error) {
	rs, err := s.Resolve(ctx, name, vc, projectRoot)
	if err != nil {
		return nil, "", err
	}
	if rs == nil {
		return nil, "", fmt.Errorf("skill %q not found", name)
	}
	if rs.Dir != "" {
		files, err := ListDirFiles(rs.Dir)
		return files, rs.Dir, err
	}
	// DB skill — delegate to store's ListFiles (needs skill ID).
	// The store interface doesn't expose ListFiles with paths only, so we
	// go through the underlying store if available.
	return nil, "", nil
}

// ListDirFiles walks a directory and returns relative file paths.
func ListDirFiles(dir string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	return files, err
}

// IsWritable returns whether a skill scope supports write operations.
func IsWritable(scope string) bool {
	return scope == "user" || scope == "agent"
}
