package skills

import (
	"context"
	"encoding/json"
	"time"
)

const MainFile = "SKILL.md"

// Skill represents a skill row (metadata only, no file content).
type Skill struct {
	ID                     string
	Scope                  string // system | system_agent | user | user_agent
	UserID                 string
	AgentID                string
	Name                   string
	Description            string
	Status                 string // active | deprecated (legacy rows only)
	DisableModelInvocation bool
	Metadata               json.RawMessage
	CreatedAt              time.Time
	UpdatedAt              time.Time
	Version                int64
	// ContentDigest is the canonical Home-managed content revision. PostgreSQL-backed
	// rows deliberately leave it empty because PostgreSQL is not content authority.
	ContentDigest string
}

// SkillSnapshot is the committed representation returned by an atomic Skill
// mutation. Files contains the complete retained path set from that transaction.
type SkillSnapshot struct {
	Skill Skill
	Files []string
}

// ViewContext describes who is asking and from where.
// Empty fields mean no such context (e.g. empty UserID → only system skills visible).
type ViewContext struct {
	UserID  string
	AgentID string
}

// UpdatePatch carries optional updates for a skill's metadata fields.
type UpdatePatch struct {
	Description            *string
	Status                 *string
	DisableModelInvocation *bool
	Metadata               json.RawMessage // optional; set to overwrite
}

// ManagedSkillCursor identifies the last visible row in a stable lifecycle page.
type ManagedSkillCursor struct {
	Timestamp time.Time
	ID        string
}

// ManagedSkillUpdate applies one atomic metadata/file lifecycle mutation.
type ManagedSkillUpdate struct {
	ID      string
	UserID  string
	AgentID string
	Scope   string
	// ExpectedDigest is the exact Home content revision the caller inspected.
	ExpectedDigest string
	Patch          UpdatePatch
	Files          map[string]string
	// DeleteFiles are removed in the same committed revision as Patch and Files.
	DeleteFiles     []string
	ConvertToManual bool
}

// ManagedSkillDelete withdraws one exact mutable Skill revision. Scope and
// owner facts must be copied from the Skill the caller authorized.
type ManagedSkillDelete struct {
	ID             string
	UserID         string
	AgentID        string
	Scope          string
	ExpectedDigest string
}

// ManagedSkillFileDelete removes one companion file from an exact mutable
// Skill revision. It returns the retained committed Skill snapshot.
type ManagedSkillFileDelete struct {
	ManagedSkillDelete
	Path string
}

// Store is the persistence interface for skills.
type Store interface {
	// Get loads one exact durable Skill by ID. Callers authorize its returned
	// scope and owner facts; an ID alone is never authority.
	Get(ctx context.Context, id string) (*Skill, error)

	// List returns all visible skills for the given context (metadata only, no file content).
	List(ctx context.Context, vc ViewContext) ([]Skill, error)

	// ListAll returns every skill regardless of status or visibility (internal admin use only).
	ListAll(ctx context.Context) ([]Skill, error)

	// ListActiveReflectOwnedUserAgentSkills returns the Reflect-managed active
	// skills that #531 is allowed to consider for one user-agent context.
	ListActiveReflectOwnedUserAgentSkills(ctx context.Context, userID string, agentID string) ([]Skill, error)

	// CreateManagedSkill atomically creates a Skill and returns its committed state.
	CreateManagedSkill(ctx context.Context, s Skill, files map[string]string) (SkillSnapshot, error)

	// UpdateManagedSkill atomically patches a live mutable skill and its files.
	UpdateManagedSkill(ctx context.Context, in ManagedSkillUpdate) (SkillSnapshot, error)

	// DeleteManagedSkill atomically withdraws an exact mutable Skill revision.
	DeleteManagedSkill(ctx context.Context, in ManagedSkillDelete) error

	// DeleteManagedSkillFile atomically removes one companion file and returns
	// the retained committed Skill snapshot.
	DeleteManagedSkillFile(ctx context.Context, in ManagedSkillFileDelete) (SkillSnapshot, error)

	// CreateReflectOwnedUserAgentSkill creates an active user_agent skill whose
	// lifecycle is owned by Reflect.
	CreateReflectOwnedUserAgentSkill(ctx context.Context, in ReflectSkillCreate) (Skill, error)

	// PatchReflectOwnedUserAgentSkill updates a Reflect-owned user_agent skill
	// under optimistic content revision control.
	PatchReflectOwnedUserAgentSkill(ctx context.Context, in ReflectSkillPatch) (Skill, error)

	// DeleteReflectOwnedUserAgentSkill permanently deletes a stale Reflect-owned
	// user_agent skill after rechecking its version, usage, and pair activity.
	DeleteReflectOwnedUserAgentSkill(ctx context.Context, in ReflectSkillDelete) (Skill, error)

	// TouchReflectSkillRuntimeUse records a successful runtime load of a
	// Reflect-owned user_agent skill. Implementations recheck owner/status.
	TouchReflectSkillRuntimeUse(ctx context.Context, skillID string, userID string, agentID string, contentDigest string) error

	// ListForAgentContext returns system, agent, and current-user skills for one agent.
	ListForAgentContext(ctx context.Context, userID string, agentID string) ([]Skill, error)

	// ListByScope returns every skill in exactly one scope/owner bucket (including
	// drafts and disabled skills) for management views.
	ListByScope(ctx context.Context, scope string, userID string, agentID string) ([]Skill, error)

	// ListForAdmin returns system and agent skills, plus the admin user's own user skills.
	ListForAdmin(ctx context.Context, userID string) ([]Skill, error)

	// ListForUser returns skills visible to a non-admin user across accessible agents.
	ListForUser(ctx context.Context, userID string, agentIDs []string) ([]Skill, error)

	// Resolve finds the highest-priority visible skill by name.
	// Priority: user_agent > user > system_agent > system.
	Resolve(ctx context.Context, name string, vc ViewContext) (*Skill, error)

	// LoadFile fetches a single file by path. Pass MainFile ("SKILL.md") for the body.
	LoadFile(ctx context.Context, skillID, path string) (string, error)

	// ListFiles returns all file paths for a skill (no content).
	ListFiles(ctx context.Context, skillID string) ([]string, error)

	// ListFilesWithContent returns all files for a skill keyed by path.
	ListFilesWithContent(ctx context.Context, skillID string) (map[string]string, error)
}
