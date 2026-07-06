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

// Resolve finds a skill by name across all 4 levels, honoring the scope
// precedence: project > user_agent > user > system_agent > system. Project
// (filesystem) wins outright; DB skills (which already rank user_agent > user >
// system_agent > system among themselves) shadow filesystem system skills.
func (s *Service) Resolve(ctx context.Context, name string, vc pkgplugins.SkillViewContext, projectRoot string) (*ResolvedSkill, error) {
	if projectRoot != "" {
		if rs := findFSSkill(projectRoot, "project", name); rs != nil {
			return rs, nil
		}
	}

	if s.store != nil {
		sk, err := s.store.Resolve(ctx, name, vc)
		if err != nil {
			return nil, err
		}
		if sk != nil {
			return &ResolvedSkill{Skill: *sk}, nil
		}
	}

	if s.stellaHome != "" {
		if rs := findFSSkill(s.stellaHome, "system", name); rs != nil {
			return rs, nil
		}
	}
	return nil, nil
}

// findFSSkill returns the named skill from a filesystem scope root, or nil.
func findFSSkill(root, scope, name string) *ResolvedSkill {
	skills, dirs, err := listFSSkills(root, scope)
	if err != nil {
		return nil
	}
	for _, sk := range skills {
		if sk.Name == name {
			return &ResolvedSkill{Skill: sk, Dir: dirs[name]}
		}
	}
	return nil
}

// ResolveScoped finds a skill by name in a specific scope, for management
// (get/update/delete/file) operations. Unlike Resolve it never falls through to
// another scope, and it matches by row ownership rather than runtime visibility
// so it also finds drafts, disabled (knowledge) entries, and DB-backed system
// skills that the effective Resolve query filters out.
func (s *Service) ResolveScoped(ctx context.Context, name, scope string, vc pkgplugins.SkillViewContext, projectRoot string) (*ResolvedSkill, error) {
	switch scope {
	case "project":
		return findFSSkill(projectRoot, "project", name), nil
	case "system_agent", "user", "user_agent":
		return s.dbSkillByScope(ctx, name, scope, vc)
	case "system":
		// A system skill may live in the DB (installed via Settings) or on the
		// filesystem (built-in). Prefer the DB row, fall back to the FS.
		rs, err := s.dbSkillByScope(ctx, name, scope, vc)
		if err != nil {
			return nil, err
		}
		if rs != nil {
			return rs, nil
		}
		return findFSSkill(s.stellaHome, "system", name), nil
	default:
		return nil, nil
	}
}

// dbSkillByScope returns the DB skill with the given name in exactly one
// scope/owner bucket, including drafts and disabled entries.
func (s *Service) dbSkillByScope(ctx context.Context, name, scope string, vc pkgplugins.SkillViewContext) (*ResolvedSkill, error) {
	if s.store == nil {
		return nil, nil
	}
	userID, agentID := scopeOwner(scope, vc)
	list, err := s.store.ListByScope(ctx, scope, userID, agentID)
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].Name == name {
			return &ResolvedSkill{Skill: list[i]}, nil
		}
	}
	return nil, nil
}

// scopeOwner derives the (user_id, agent_id) owner keys a scope is bucketed by.
func scopeOwner(scope string, vc pkgplugins.SkillViewContext) (userID, agentID string) {
	switch scope {
	case "system_agent":
		return "", vc.AgentID
	case "user":
		return vc.UserID, ""
	case "user_agent":
		return vc.UserID, vc.AgentID
	default: // system
		return "", ""
	}
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
	if s.store != nil && rs.ID != "" {
		files, err := s.store.ListFiles(ctx, rs.ID)
		return files, "", err
	}
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
	return scope == "user" || scope == "user_agent" || scope == "system_agent"
}
