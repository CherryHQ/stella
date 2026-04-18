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
	Scope                  string // system | agent | user
	UserID                 int64
	AgentID                string
	Name                   string
	Description            string
	Status                 string // draft | active | deprecated
	DisableModelInvocation bool
	Metadata               json.RawMessage
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

// ViewContext describes who is asking and from where.
// Empty fields mean no such context (e.g. UserID=0 → only system skills visible).
type ViewContext struct {
	UserID  int64
	AgentID string
}

// UpdatePatch carries optional updates for a skill's metadata fields.
type UpdatePatch struct {
	Description            *string
	Status                 *string
	DisableModelInvocation *bool
	Metadata               json.RawMessage // optional; set to overwrite
}

// Store is the persistence interface for skills.
type Store interface {
	// List returns all visible skills for the given context (metadata only, no file content).
	List(ctx context.Context, vc ViewContext) ([]Skill, error)

	// ListAll returns every skill regardless of status or visibility (admin use only).
	ListAll(ctx context.Context) ([]Skill, error)

	// Resolve finds the highest-priority visible skill by name.
	// Priority: user > agent > system.
	Resolve(ctx context.Context, name string, vc ViewContext) (*Skill, error)

	// LoadFile fetches a single file by path. Pass MainFile ("SKILL.md") for the body.
	LoadFile(ctx context.Context, skillID, path string) (string, error)

	// ListFiles returns all file paths for a skill (no content).
	ListFiles(ctx context.Context, skillID string) ([]string, error)

	// Create inserts the skill row and all its files (must include "SKILL.md").
	Create(ctx context.Context, s Skill, files map[string]string) (string, error)

	// Update patches metadata fields. Use UpsertFile to change file content.
	Update(ctx context.Context, id string, patch UpdatePatch) error

	// UpsertFile creates or replaces a single file under a skill.
	UpsertFile(ctx context.Context, skillID, path, content string) error

	DeleteFile(ctx context.Context, skillID, path string) error
	Delete(ctx context.Context, id string) error

	// ExpireDrafts deprecates all draft skills whose created-at timestamp is
	// before the given cutoff.
	ExpireDrafts(ctx context.Context, before time.Time) error
}
