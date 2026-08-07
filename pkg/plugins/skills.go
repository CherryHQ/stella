package plugins

import (
	"context"
	"encoding/json"
	"time"
)

const SkillMainFile = "SKILL.md"

// Skill represents a skill row (metadata only, no file content).
type Skill struct {
	ID                     string
	Scope                  string // system | agent | user | project (project is filesystem-only)
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
	// ContentDigest identifies the exact canonical Home-managed content revision.
	// It is empty when the plugin-facing Skill does not come from a Home catalog.
	ContentDigest string
}

// SkillViewContext describes who is asking and from where.
// Empty fields mean no such context (e.g. empty UserID → only system skills visible).
type SkillViewContext struct {
	UserID            string
	AgentID           string
	DisabledSkillRefs []string // immutable AgentSkillPolicy snapshot for this operation
}

// SkillUpdatePatch carries optional updates for a skill's metadata fields.
type SkillUpdatePatch struct {
	Description            *string
	DisableModelInvocation *bool
	Metadata               json.RawMessage // optional; set to overwrite
}

// ManagedSkillSnapshot is the committed representation of an atomic plugin
// Skill mutation.
type ManagedSkillSnapshot struct {
	Skill Skill
	Files []string
}

// ManagedSkillUpdate applies metadata and file changes to the exact revision
// the plugin previously resolved or authorized.
type ManagedSkillUpdate struct {
	ID              string
	UserID          string
	AgentID         string
	Scope           string
	ExpectedDigest  string
	Patch           SkillUpdatePatch
	Files           map[string]string
	DeleteFiles     []string
	ConvertToManual bool
}

type ManagedSkillDelete struct {
	ID             string
	UserID         string
	AgentID        string
	Scope          string
	ExpectedDigest string
}

type ManagedSkillFileDelete struct {
	ManagedSkillDelete
	Path string
}

// HomeSkillFile is one atomic Home-authoritative Skill read. Directory is a
// validated POSIX path relative to the selected catalog root; it is never a
// host filesystem coordinate.
type HomeSkillFile struct {
	Skill      Skill
	Content    string
	Directory  string
	Suppressed bool // an active Home winner disables invocation and shadows lower scopes
}

// HomeSkillFileLoader is deliberately narrower than SkillStore. Home-backed
// runtime callers use it to keep content and execution directory pinned to one
// catalog descriptor; legacy stores continue to serve LoadFile only.
type HomeSkillFileLoader interface {
	LoadHomeSkillFile(ctx context.Context, name, path string, vc SkillViewContext) (*HomeSkillFile, error)
}

// SkillStore is the plugin-facing persistence interface for skills, available
// via Platform. It intentionally exposes the management/runtime subset; internal
// Reflect lifecycle methods stay on internal/skills.Store.
type SkillStore interface {
	// List returns all visible skills for the given context (metadata only, no file content).
	List(ctx context.Context, vc SkillViewContext) ([]Skill, error)

	// Resolve finds the highest-priority visible skill by name.
	// Priority: user > agent > system (project skills are resolved via filesystem).
	Resolve(ctx context.Context, name string, vc SkillViewContext) (*Skill, error)

	// ListByScope returns every skill in exactly one scope/owner bucket,
	// including disabled skills, for exact management lookups.
	ListByScope(ctx context.Context, scope, userID, agentID string) ([]Skill, error)

	// LoadFile fetches a single file by path. Pass SkillMainFile ("SKILL.md") for the body.
	LoadFile(ctx context.Context, skillID, path string) (string, error)

	// ListFiles returns all file paths for a skill (no content).
	ListFiles(ctx context.Context, skillID string) ([]string, error)

	// CreateManagedSkill inserts the skill row and all its files atomically.
	CreateManagedSkill(ctx context.Context, s Skill, files map[string]string) (ManagedSkillSnapshot, error)

	// UpdateManagedSkill atomically applies metadata and all file changes.
	UpdateManagedSkill(ctx context.Context, in ManagedSkillUpdate) (ManagedSkillSnapshot, error)
	DeleteManagedSkill(ctx context.Context, in ManagedSkillDelete) error
	DeleteManagedSkillFile(ctx context.Context, in ManagedSkillFileDelete) (ManagedSkillSnapshot, error)
}
