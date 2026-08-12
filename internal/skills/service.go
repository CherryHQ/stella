package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"

	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
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
	Dir      string // logical path for project Skills; host path only for immutable builtins/runtime DB cache
	project  *ProjectSnapshot
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

// ImmutableFiles lists files from an immutable builtin or project snapshot.
// A nil result means the Skill is backed by the trusted runtime cache instead.
func (s *ResolvedSkill) ImmutableFiles() []string {
	if s.project != nil {
		files, _, err := s.project.listFiles(s.Name)
		if err != nil {
			return []string{}
		}
		return files
	}
	return s.BuiltinFiles()
}

func (s *ResolvedSkill) LoadImmutableFile(filePath string) (string, error) {
	if s.project != nil {
		data, _, err := s.project.load(s.Name, filePath)
		return data, err
	}
	return s.LoadBuiltinFile(filePath)
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
	return filterDisabled(s.mergeSkills(dbSkills, projectSnapshotFromContext(ctx)), vc.DisabledSkillRefs), nil
}

// ListMergedWithDB merges the given DB skills with FS skills.
// Use this when the caller needs a different DB query (e.g. including disabled skills).
func (s *Service) ListMergedWithDB(dbSkills []pkgplugins.Skill, projectRoot string) []ResolvedSkill {
	return s.mergeSkills(dbSkills, nil)
}

func (s *Service) ListMergedWithDBSnapshot(dbSkills []pkgplugins.Skill, snapshot *ProjectSnapshot) []ResolvedSkill {
	return s.mergeSkills(dbSkills, snapshot)
}

func (s *Service) mergeSkills(dbSkills []pkgplugins.Skill, snapshot *ProjectSnapshot) []ResolvedSkill {
	projSkills, projDirs := snapshot.list()
	builtinSkills, err := s.builtinSkills()
	if err != nil {
		return nil
	}

	seen := make(map[string]bool, len(projSkills)+len(dbSkills)+len(builtinSkills))
	out := make([]ResolvedSkill, 0, len(projSkills)+len(dbSkills)+len(builtinSkills))

	for _, sk := range projSkills {
		seen[sk.Name] = true
		out = append(out, ResolvedSkill{Skill: sk, Dir: path.Join(snapshot.logicalRoot, projDirs[sk.Name]), project: snapshot})
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
	return out
}

// Resolve finds a skill by name across all 4 levels, honoring the scope
// precedence: project > user_agent > user > system_agent > system. Project
// (filesystem) wins outright; DB skills (which already rank user_agent > user >
// system_agent > system among themselves) shadow filesystem system skills.
func (s *Service) Resolve(ctx context.Context, name string, vc pkgplugins.SkillViewContext, projectRoot string) (*ResolvedSkill, error) {
	if builtinName, ok := s.builtinNameForReference(name); ok {
		name = builtinName
	}
	if snapshot := projectSnapshotFromContext(ctx); snapshot != nil {
		if rs := findProjectSkill(snapshot, name); rs != nil {
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

func findProjectSkill(snapshot *ProjectSnapshot, name string) *ResolvedSkill {
	skills, dirs := snapshot.list()
	for _, sk := range skills {
		if sk.Name == name {
			return &ResolvedSkill{Skill: sk, Dir: path.Join(snapshot.logicalRoot, dirs[name]), project: snapshot}
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
		return findProjectSkill(projectSnapshotFromContext(ctx), name), nil
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
	if path == "" {
		path = pkgplugins.SkillMainFile
	}

	rs, err := s.Resolve(ctx, name, vc, projectRoot)
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
	if rs.project != nil {
		data, logicalDir, err := rs.project.load(rs.Name, path)
		if err != nil {
			return "", "", nil, fmt.Errorf("load %s skill %q file %q: %w", rs.Scope, name, path, err)
		}
		return data, logicalDir, rs, nil
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
	if rs.project != nil {
		return rs.project.listFiles(rs.Name)
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
	return scope == "user" || scope == "user_agent" || scope == "system" || scope == "system_agent"
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
