package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	"github.com/CherryHQ/stella/pkg/sandbox"
	"github.com/CherryHQ/stella/resources"
)

// Service provides unified skill resolution across all 4 levels
// (project, user, agent, system). Both the CLI tool and HTTP server
// use this to avoid duplicating the merge/resolve logic.
type Service struct {
	store       pkgplugins.SkillStore
	stellaHome  string
	registry    *resources.Registry
	registryErr error
}

func NewService(store pkgplugins.SkillStore, stellaHome string, registries ...*resources.Registry) *Service {
	var registry *resources.Registry
	if len(registries) > 0 {
		registry = registries[0]
	}
	var err error
	if registry == nil {
		registry, err = resources.Default()
	}
	return &Service{store: store, stellaHome: stellaHome, registry: registry, registryErr: err}
}

// ResolvedSkill is a skill with its filesystem directory (if applicable).
type ResolvedSkill struct {
	pkgplugins.Skill
	Dir      string // project/builtin directory; Home Skill directories stay opaque here
	homeDir  string // validated catalog-root-relative POSIX execution directory
	builtin  *resources.BuiltinSkillDescriptor
	registry *resources.Registry
}

func (s *ResolvedSkill) LoadBuiltinFile(filePath string) (string, error) {
	if s.builtin == nil || s.registry == nil {
		return "", fmt.Errorf("not a builtin skill")
	}
	data, _, err := s.registry.ReadBuiltinSkillFile(s.builtin.Name, filePath)
	return string(data), err
}

func (s *ResolvedSkill) BuiltinFiles() []string {
	if s.builtin == nil {
		return nil
	}
	out := make([]string, 0, len(s.builtin.Files))
	for _, file := range s.builtin.Files {
		out = append(out, file.Path)
	}
	return out
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
	merged, err := s.mergeSkills(ctx, dbSkills, hostProjectSource{root: projectRoot}, true)
	if err != nil {
		return nil, err
	}
	return filterDisabled(merged, vc.DisabledSkillRefs), nil
}

// ListMergedWithDB merges the given DB skills with FS skills.
// Use this when the caller needs a different DB query (e.g. including disabled skills).
func (s *Service) ListMergedWithDB(dbSkills []pkgplugins.Skill, projectRoot string) []ResolvedSkill {
	merged, _ := s.mergeSkills(context.Background(), dbSkills, hostProjectSource{root: projectRoot}, true)
	return merged
}

// ListMergedFromFilesystem resolves the runner project tier through its injected
// filesystem. It intentionally has no host-project fallback.
func (s *Service) ListMergedFromFilesystem(ctx context.Context, vc pkgplugins.SkillViewContext, filesystem sandbox.Filesystem, projectRoot string) ([]ResolvedSkill, error) {
	project, err := newFilesystemProjectSource(filesystem, projectRoot)
	if err != nil {
		return nil, err
	}
	var dbSkills []pkgplugins.Skill
	if s.store != nil {
		if dbSkills, err = s.store.List(ctx, vc); err != nil {
			return nil, fmt.Errorf("list db skills: %w", err)
		}
	}
	merged, err := s.mergeSkills(ctx, dbSkills, project, false)
	if err != nil {
		return nil, err
	}
	return filterDisabled(merged, vc.DisabledSkillRefs), nil
}

func (s *Service) LoadFileFromFilesystem(ctx context.Context, name, file string, vc pkgplugins.SkillViewContext, filesystem sandbox.Filesystem, projectRoot string) (string, string, *ResolvedSkill, error) {
	project, err := newFilesystemProjectSource(filesystem, projectRoot)
	if err != nil {
		return "", "", nil, err
	}
	return s.loadFile(ctx, name, file, vc, project, false)
}

func (s *Service) mergeSkills(ctx context.Context, dbSkills []pkgplugins.Skill, project projectSkillSource, ignoreProjectError bool) ([]ResolvedSkill, error) {
	projSkills, projDirs, projectErr := project.list(ctx)
	if projectErr != nil && !ignoreProjectError {
		return nil, projectErr
	}
	if projectErr != nil {
		projSkills, projDirs = nil, nil
	}
	builtinSkills, err := s.builtinSkills()
	if err != nil {
		if ignoreProjectError {
			return nil, nil
		}
		return nil, err
	}

	seen := make(map[string]bool, len(projSkills)+len(dbSkills)+len(builtinSkills))
	out := make([]ResolvedSkill, 0, len(projSkills)+len(dbSkills)+len(builtinSkills))

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
	for _, sk := range builtinSkills {
		if seen[sk.Name] {
			continue
		}
		seen[sk.Name] = true
		out = append(out, sk)
	}
	return out, nil
}

// Resolve finds a skill by name across all 4 levels, honoring the scope
// precedence: project > user_agent > user > system_agent > system. Project
// (filesystem) wins outright; DB skills (which already rank user_agent > user >
// system_agent > system among themselves) shadow filesystem system skills.
func (s *Service) Resolve(ctx context.Context, name string, vc pkgplugins.SkillViewContext, projectRoot string) (*ResolvedSkill, error) {
	return s.resolve(ctx, name, vc, hostProjectSource{root: projectRoot}, true)
}

func (s *Service) resolve(ctx context.Context, name string, vc pkgplugins.SkillViewContext, project projectSkillSource, ignoreProjectError bool) (*ResolvedSkill, error) {
	if builtinName, ok := s.builtinNameForReference(name); ok {
		name = builtinName
	}
	if project != nil {
		if rs, err := findProjectSkill(ctx, project, name); err != nil && !ignoreProjectError {
			return nil, err
		} else if rs != nil {
			return filterResolved(rs, vc.DisabledSkillRefs), nil
		}
	}

	if s.store != nil {
		sk, err := s.store.Resolve(ctx, name, vc)
		if err != nil {
			return nil, err
		}
		if sk != nil {
			return filterResolved(&ResolvedSkill{Skill: *sk}, vc.DisabledSkillRefs), nil
		}
	}
	rs, err := s.builtinSkill(name)
	if err != nil {
		return nil, err
	}
	return filterResolved(rs, vc.DisabledSkillRefs), nil
}

func findProjectSkill(ctx context.Context, project projectSkillSource, name string) (*ResolvedSkill, error) {
	skills, dirs, err := project.list(ctx)
	if err != nil {
		return nil, err
	}
	for _, sk := range skills {
		if sk.Name == name {
			return &ResolvedSkill{Skill: sk, Dir: dirs[sk.Name]}, nil
		}
	}
	return nil, nil
}

// filterDisabled runs only after precedence resolution. A disabled winner is
// absent; callers must never retry a lower-precedence implementation by name.
func filterDisabled(in []ResolvedSkill, disabled []string) []ResolvedSkill {
	if len(disabled) == 0 {
		return in
	}
	out := make([]ResolvedSkill, 0, len(in))
	for _, rs := range in {
		if !isDisabled(rs, disabled) {
			out = append(out, rs)
		}
	}
	return out
}

func filterResolved(rs *ResolvedSkill, disabled []string) *ResolvedSkill {
	if rs == nil || !isDisabled(*rs, disabled) {
		return rs
	}
	return nil
}

func isDisabled(rs ResolvedSkill, disabled []string) bool {
	ref, ok := PolicyRef(rs)
	return ok && slices.Contains(disabled, ref)
}

// PolicyRef returns the stable policy identity for policy-addressable Skills.
func PolicyRef(rs ResolvedSkill) (string, bool) {
	if rs.builtin != nil {
		return "builtin:" + rs.Name, true
	}
	switch rs.Scope {
	case "system", "system_agent":
		return rs.Scope + ":" + rs.Name, true
	default:
		return "", false
	}
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
// so it also finds model-disabled entries and DB-backed system Skills that the
// effective Resolve query filters out.
func (s *Service) ResolveScoped(ctx context.Context, name, scope string, vc pkgplugins.SkillViewContext, projectRoot string) (*ResolvedSkill, error) {
	switch scope {
	case "builtin":
		return s.builtinSkill(name)
	case "project":
		return findFSSkill(projectRoot, "project", name), nil
	case "system_agent", "user", "user_agent":
		return s.dbSkillByScope(ctx, name, scope, vc)
	case "system":
		// A system skill may live in the DB (installed via Settings) or in the
		// immutable release Registry. Prefer the DB row, then the Registry.
		rs, err := s.dbSkillByScope(ctx, name, scope, vc)
		if err != nil {
			return nil, err
		}
		if rs != nil {
			return rs, nil
		}
		return s.builtinSkill(name)
	default:
		return nil, nil
	}
}

// dbSkillByScope returns the DB skill with the given name in exactly one
// scope/owner bucket, including model-disabled entries.
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

// LoadFile loads a file from a skill resolved by name. It returns the resolved
// record so callers can reuse the exact identity for post-load bookkeeping.
func (s *Service) LoadFile(ctx context.Context, name, path string, vc pkgplugins.SkillViewContext, projectRoot string) (content string, skillDir string, resolved *ResolvedSkill, err error) {
	return s.loadFile(ctx, name, path, vc, hostProjectSource{root: projectRoot}, true)
}

func (s *Service) loadFile(ctx context.Context, name, path string, vc pkgplugins.SkillViewContext, project projectSkillSource, ignoreProjectError bool) (content string, skillDir string, resolved *ResolvedSkill, err error) {
	if path == "" {
		path = pkgplugins.SkillMainFile
	}
	if builtinName, ok := s.builtinNameForReference(name); ok {
		name = builtinName
	}
	if project != nil {
		if rs, projectErr := findProjectSkill(ctx, project, name); projectErr != nil && !ignoreProjectError {
			return "", "", nil, fmt.Errorf("resolve project skill %q: %w", name, projectErr)
		} else if rs != nil {
			data, err := project.load(ctx, rs.Dir, path)
			if err != nil {
				return "", "", nil, fmt.Errorf("load project skill %q file %q: %w", name, path, err)
			}
			return data, rs.Dir, rs, nil
		}
	}
	// Home is the production content authority. This capability binds the
	// selected descriptor, requested bytes, digest, and execution directory in
	// one snapshot, rather than resolving then reopening a mutable link.
	if loader, ok := s.store.(pkgplugins.HomeSkillFileLoader); ok {
		loaded, err := loader.LoadHomeSkillFile(ctx, name, path, vc)
		if err != nil {
			return "", "", nil, fmt.Errorf("load Home skill %q file %q: %w", name, path, err)
		}
		if loaded != nil {
			if loaded.Suppressed {
				return "", "", nil, fmt.Errorf("skill %q not found", name)
			}
			rs := &ResolvedSkill{Skill: loaded.Skill, homeDir: loaded.Directory}
			if rs = filterResolved(rs, vc.DisabledSkillRefs); rs == nil {
				return "", "", nil, fmt.Errorf("skill %q not found", name)
			}
			return loaded.Content, "", rs, nil
		}
	}

	rs, err := s.resolve(ctx, name, vc, project, ignoreProjectError)
	if err != nil {
		return "", "", nil, fmt.Errorf("resolve skill %q: %w", name, err)
	}
	if rs == nil {
		return "", "", nil, fmt.Errorf("skill %q not found", name)
	}

	if rs.builtin != nil {
		data, err := rs.LoadBuiltinFile(path)
		if err != nil {
			return "", "", nil, fmt.Errorf("load builtin skill %q file %q: %w", name, path, err)
		}
		return data, rs.Dir, rs, nil
	}
	if rs.Dir != "" {
		data, err := project.load(ctx, rs.Dir, path)
		if err != nil {
			return "", "", nil, fmt.Errorf("load %s skill %q file %q: %w", rs.Scope, name, path, err)
		}
		return data, rs.Dir, rs, nil
	}

	if s.store == nil {
		return "", "", nil, fmt.Errorf("skill %q has no directory and store is unavailable", name)
	}
	data, err := s.store.LoadFile(ctx, rs.ID, path)
	if err != nil {
		return "", "", nil, fmt.Errorf("load skill %q file %q: %w", name, path, err)
	}
	return data, "", rs, nil
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
	if rs.builtin != nil {
		return rs.BuiltinFiles(), rs.Dir, nil
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

func (s *Service) builtinSkills() ([]ResolvedSkill, error) {
	if s.registryErr != nil {
		return nil, fmt.Errorf("load builtin registry: %w", s.registryErr)
	}
	if s.registry == nil {
		return nil, fmt.Errorf("builtin registry is unavailable")
	}
	descriptors := s.registry.BuiltinSkills()
	out := make([]ResolvedSkill, 0, len(descriptors))
	for i := range descriptors {
		rs, err := s.resolvedBuiltin(&descriptors[i])
		if err != nil {
			return nil, err
		}
		out = append(out, rs)
	}
	return out, nil
}

func (s *Service) builtinSkill(name string) (*ResolvedSkill, error) {
	if s.registryErr != nil {
		return nil, fmt.Errorf("load builtin registry: %w", s.registryErr)
	}
	if s.registry == nil {
		return nil, fmt.Errorf("builtin registry is unavailable")
	}
	descriptor, ok := s.registry.BuiltinSkill(name)
	if !ok {
		return nil, nil
	}
	rs, err := s.resolvedBuiltin(&descriptor)
	if err != nil {
		return nil, err
	}
	return &rs, nil
}

func (s *Service) builtinNameForReference(reference string) (string, bool) {
	if s.registry == nil {
		return "", false
	}
	for _, descriptor := range s.registry.BuiltinSkills() {
		if reference == descriptor.APIID || reference == descriptor.Ref {
			return descriptor.Name, true
		}
	}
	return "", false
}

func (s *Service) resolvedBuiltin(descriptor *resources.BuiltinSkillDescriptor) (ResolvedSkill, error) {
	metadata, err := json.Marshal(descriptor.Metadata)
	if err != nil {
		return ResolvedSkill{}, fmt.Errorf("encode builtin skill %q metadata: %w", descriptor.Name, err)
	}
	dir := ""
	if s.stellaHome != "" {
		bundle, err := s.registry.BundlePath(s.stellaHome)
		if err != nil {
			return ResolvedSkill{}, err
		}
		dir = filepath.Join(bundle, filepath.FromSlash(descriptor.Root))
	}
	return ResolvedSkill{
		Skill: pkgplugins.Skill{
			ID: descriptor.APIID, Scope: "system", Name: descriptor.Name,
			Description: descriptor.Description, Status: SkillStatusActive,
			DisableModelInvocation: descriptor.DisableModelInvocation, Metadata: metadata,
		},
		Dir: dir, builtin: descriptor, registry: s.registry,
	}, nil
}
