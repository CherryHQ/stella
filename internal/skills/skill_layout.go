package skills

import "path/filepath"

// SkillDiskLayout selects the runtime cache root for a DB-backed Skill by scope.
// The load path materializes authoritative DB files under the stable Skill ID
// before exposing the matching <skill_dir>.
//
// Roots are supplied by the caller because runtime sandboxes may expose
// canonicalized paths that differ from host paths. Only the per-scope mapping
// lives here.
type SkillDiskLayout struct {
	// SystemDB holds DB-installed system-scope skills. It is deliberately NOT the
	// shipped built-in dir (those resolve from the filesystem via ResolvedSkill.Dir):
	// DB system Skills must not share the shipped built-in directory.
	SystemDB string
	// Agent holds system_agent (admin-managed, agent-bound) skills.
	Agent string
	// User holds user-level skills, shared across the user's agents.
	User string
	// UserAgent holds per-(user, agent) skills.
	UserAgent string
}

// BaseDir returns the directory under which scope's runtime caches live (each
// in a stable-ID subdirectory), or "" when the scope has no cache location.
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

// Dir returns the runtime cache directory for one stable DB Skill identity.
func (l SkillDiskLayout) Dir(scope, skillID string) string {
	base := l.BaseDir(scope)
	if base == "" || skillID == "" {
		return ""
	}
	return filepath.Join(base, skillID)
}
