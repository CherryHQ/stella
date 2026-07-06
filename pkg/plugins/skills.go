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
	Status                 string // draft | active | deprecated
	DisableModelInvocation bool
	Metadata               json.RawMessage
	CreatedAt              time.Time
	UpdatedAt              time.Time
	Version                int64
}

// SkillViewContext describes who is asking and from where.
// Empty fields mean no such context (e.g. empty UserID → only system skills visible).
type SkillViewContext struct {
	UserID  string
	AgentID string
}

// SkillUpdatePatch carries optional updates for a skill's metadata fields.
type SkillUpdatePatch struct {
	Description            *string
	Status                 *string
	DisableModelInvocation *bool
	Metadata               json.RawMessage // optional; set to overwrite
}

// SkillStore is the persistence interface for skills, available to plugins via Platform.
// It mirrors internal/skills.Store method-for-method; internal/skills re-exports these
// types as aliases so both sides remain in sync.
type SkillStore interface {
	// List returns all visible skills for the given context (metadata only, no file content).
	List(ctx context.Context, vc SkillViewContext) ([]Skill, error)

	// Resolve finds the highest-priority visible skill by name.
	// Priority: user > agent > system (project skills are resolved via filesystem).
	Resolve(ctx context.Context, name string, vc SkillViewContext) (*Skill, error)

	// ListByScope returns every skill in exactly one scope/owner bucket,
	// including drafts and disabled skills, for exact management lookups.
	ListByScope(ctx context.Context, scope, userID, agentID string) ([]Skill, error)

	// LoadFile fetches a single file by path. Pass SkillMainFile ("SKILL.md") for the body.
	LoadFile(ctx context.Context, skillID, path string) (string, error)

	// ListFiles returns all file paths for a skill (no content).
	ListFiles(ctx context.Context, skillID string) ([]string, error)

	// Create inserts the skill row and all its files (must include "SKILL.md").
	Create(ctx context.Context, s Skill, files map[string]string) (string, error)

	// Update patches metadata fields. Use UpsertFile to change file content.
	Update(ctx context.Context, id string, patch SkillUpdatePatch) error

	// UpsertFile creates or replaces a single file under a skill.
	UpsertFile(ctx context.Context, skillID, path, content string) error

	DeleteFile(ctx context.Context, skillID, path string) error
	Delete(ctx context.Context, id string) error

	// ExpireDrafts deprecates all draft skills whose created-at timestamp is
	// before the given cutoff.
	ExpireDrafts(ctx context.Context, before time.Time) error
}
