package skills

import "path/filepath"

// SkillDiskLayout is the single authority for where a DB-backed skill's files
// live on disk, keyed by scope. The write side (DiskSyncStore mirrors every DB
// skill to these dirs) and the read side (the skills tool emits the matching
// <skill_dir>) both resolve through it, so a skill can never materialize at one
// path while its skill_dir points at another.
//
// Roots are supplied by the caller because they legitimately differ: the writer
// roots from config.StellaHome(); the runner from the sandbox's canonicalized
// (symlink-evaluated) paths, so the later host→sandbox view match holds. Only
// the per-scope mapping — which root, and the layout under it — lives here.
type SkillDiskLayout struct {
	// SystemDB holds DB-installed system-scope skills. It is deliberately NOT the
	// shipped built-in dir (those resolve from the filesystem via ResolvedSkill.Dir):
	// SyncAllToDisk deletes disk files absent from the DB, so mirroring DB system
	// skills into the built-in dir would wipe the built-ins.
	SystemDB string
	// Agent holds system_agent (admin-managed, agent-bound) skills.
	Agent string
	// User holds user-level skills, shared across the user's agents.
	User string
	// UserAgent holds per-(user, agent) skills.
	UserAgent string
}

// BaseDir returns the directory under which scope's skills live (each in a
// {name}/ subdir), or "" when the scope has no managed disk location.
func (l SkillDiskLayout) BaseDir(scope string) string {
	switch scope {
	case "system":
		return l.SystemDB
	case "system_agent":
		return l.Agent
	case "user":
		return l.User
	case "user_agent":
		return l.UserAgent
	default:
		return ""
	}
}

// Dir returns the absolute directory holding skillName's files for scope, or ""
// when the scope has no managed disk location.
func (l SkillDiskLayout) Dir(scope, skillName string) string {
	base := l.BaseDir(scope)
	if base == "" || skillName == "" {
		return ""
	}
	return filepath.Join(base, skillName)
}
