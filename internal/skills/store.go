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
	Status                 string // draft | active | deprecated
	DisableModelInvocation bool
	Metadata               json.RawMessage
	CreatedAt              time.Time
	UpdatedAt              time.Time
	Version                int64
}

type SkillChangelog struct {
	ID            string
	SkillID       string
	UserID        string
	AgentID       string
	Scope         string
	Action        string
	VersionBefore int64
	VersionAfter  int64
	Metadata      json.RawMessage
	CreatedAt     time.Time
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

// ManagedSkillState selects live or recoverable managed skill records.
type ManagedSkillState string

const (
	// ManagedSkillStateActive includes every non-deprecated status, including draft.
	ManagedSkillStateActive ManagedSkillState = "active"
	// ManagedSkillStateRemoved includes only qualifying, recoverable deprecations.
	ManagedSkillStateRemoved ManagedSkillState = "removed"
)

// ManagedSkillItem carries lifecycle details for a managed skill listing.
type ManagedSkillItem struct {
	Skill           Skill
	CreatedBy       string
	RemovalSource   string
	DeprecatedAt    *time.Time
	RestoreDeadline *time.Time
	IsRestorable    bool
}

// ManagedSkillCursor identifies the last visible row in a stable lifecycle page.
type ManagedSkillCursor struct {
	Timestamp time.Time
	ID        string
}

// ManagedSkillListQuery scopes a lifecycle list to one caller's owner buckets.
type ManagedSkillListQuery struct {
	UserID    string
	AgentID   string
	Scopes    []string
	CreatedBy string
	State     ManagedSkillState
	Limit     int32
	Now       time.Time
	Cursor    *ManagedSkillCursor
}

// ManagedSkillPage carries the complete matching count and the next keyset position.
type ManagedSkillPage struct {
	Items      []ManagedSkillItem
	Total      int64
	HasMore    bool
	NextCursor *ManagedSkillCursor
}

// ManagedSkillDeprecate identifies a mutable skill and the manual operator.
type ManagedSkillDeprecate struct {
	ID           string
	UserID       string
	AgentID      string
	Scope        string
	DeprecatedBy string
}

// ManagedSkillRestore identifies a retained skill and the restore instant.
type ManagedSkillRestore struct {
	ID         string
	UserID     string
	AgentID    string
	Scope      string
	RestoredBy string
	Now        time.Time
}

// ManagedSkillRestoreResult distinguishes an idempotent active-row restore.
type ManagedSkillRestoreResult struct {
	Skill    Skill
	Restored bool
}

// ManagedSkillUpdate applies one atomic metadata/file lifecycle mutation.
type ManagedSkillUpdate struct {
	ID              string
	UserID          string
	AgentID         string
	Scope           string
	Patch           UpdatePatch
	Files           map[string]string
	ConvertToManual bool
}

// Store is the persistence interface for skills.
type Store interface {
	// List returns all visible skills for the given context (metadata only, no file content).
	List(ctx context.Context, vc ViewContext) ([]Skill, error)

	// ListAll returns every skill regardless of status or visibility (internal admin use only).
	ListAll(ctx context.Context) ([]Skill, error)

	// ListActiveReflectOwnedUserAgentSkills returns the Reflect-managed active
	// skills that #531 is allowed to consider for one user-agent context.
	ListActiveReflectOwnedUserAgentSkills(ctx context.Context, userID string, agentID string) ([]Skill, error)

	// ListManagedSkills returns non-deprecated or recoverable lifecycle rows.
	ListManagedSkills(ctx context.Context, in ManagedSkillListQuery) (ManagedSkillPage, error)
	// DeprecateManagedSkill retains a mutable skill for its recovery window.
	DeprecateManagedSkill(ctx context.Context, in ManagedSkillDeprecate) (Skill, error)
	// RestoreManagedSkill restores a qualifying retained skill in place.
	RestoreManagedSkill(ctx context.Context, in ManagedSkillRestore) (ManagedSkillRestoreResult, error)
	// UpdateManagedSkill atomically patches a live mutable skill and its files.
	UpdateManagedSkill(ctx context.Context, in ManagedSkillUpdate) (Skill, error)

	// CreateReflectOwnedUserAgentSkill creates an active user_agent skill whose
	// lifecycle is owned by Reflect, including version and changelog records.
	CreateReflectOwnedUserAgentSkill(ctx context.Context, in ReflectSkillCreate) (Skill, error)

	// PatchReflectOwnedUserAgentSkill updates a Reflect-owned user_agent skill
	// under optimistic version control and records a changelog entry.
	PatchReflectOwnedUserAgentSkill(ctx context.Context, in ReflectSkillPatch) (Skill, error)

	// DeprecateReflectOwnedUserAgentSkill marks a Reflect-owned user_agent skill
	// as deprecated under optimistic version control and removes usage tracking.
	DeprecateReflectOwnedUserAgentSkill(ctx context.Context, in ReflectSkillDeprecate) (Skill, error)

	// RestoreReflectOwnedUserAgentSkill restores a usage-curator-deprecated
	// Reflect-owned user_agent skill for internal/admin recovery.
	RestoreReflectOwnedUserAgentSkill(ctx context.Context, in ReflectSkillRestore) (ReflectSkillRestoreResult, error)

	// TouchReflectSkillRuntimeUse records a successful runtime load of a
	// Reflect-owned user_agent skill. Implementations recheck owner/status.
	TouchReflectSkillRuntimeUse(ctx context.Context, skillID string, userID string, agentID string) error

	// ListSkillChangelogBySkill returns recent version changes for one skill.
	ListSkillChangelogBySkill(ctx context.Context, skillID string, limit int) ([]SkillChangelog, error)

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

	// Create inserts the skill row and all its files (must include "SKILL.md").
	Create(ctx context.Context, s Skill, files map[string]string) (string, error)

	// Update patches metadata fields. Use UpsertFile to change file content.
	Update(ctx context.Context, id string, vc ViewContext, patch UpdatePatch) error

	// UpsertFile creates or replaces a single file under a skill.
	UpsertFile(ctx context.Context, skillID, path, content string) error

	DeleteFile(ctx context.Context, skillID, path string) error
	Delete(ctx context.Context, id string, vc ViewContext) error

	// ExpireDrafts deprecates draft skills whose created-at timestamp is before
	// the given cutoff. Knowledge facts live in the facts table, not skills.
	ExpireDrafts(ctx context.Context, before time.Time) error
}
