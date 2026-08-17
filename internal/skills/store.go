package skills

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
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
	Status                 string // active | deprecated
	DisableModelInvocation bool
	Metadata               json.RawMessage
	CreatedAt              time.Time
	UpdatedAt              time.Time
	Version                int64
	ContentDigest          string
}

// SkillSnapshot is the committed representation returned by an atomic Skill
// mutation. Files contains the complete retained path set from that transaction.
type SkillSnapshot struct {
	Skill Skill
	Files []string
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
	ContentDigest string
	Metadata      json.RawMessage
	CreatedAt     time.Time
}

// ViewContext describes who is asking and from where.
// Empty fields mean no such context (e.g. empty UserID → only system skills visible).
type ViewContext struct {
	UserID            string
	AgentID           string
	DisabledSkillRefs []string
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
	ID              string
	UserID          string
	AgentID         string
	Scope           string
	Patch           UpdatePatch
	Files           map[string]string
	DeleteFiles     []string
	ConvertToManual bool
	ExpectedDigest  string
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

// ManagedRevision is one fully verified immutable Home revision. Files and
// Modes are the complete bounded file tree and ContentDigest identifies these
// exact bytes and modes.
type ManagedRevision struct {
	Skill Skill
	Files map[string][]byte
	Modes map[string]fs.FileMode
}

// IdentityReader exposes PostgreSQL identity inventory separately from Home
// current state so consumers can authorize an actor before opening Home.
type IdentityReader interface {
	GetIdentity(context.Context, string) (*Skill, error)
	ListIdentityVisible(context.Context, ViewContext) ([]Skill, error)
	ListIdentityByScope(context.Context, string, string, string) ([]Skill, error)
	ListIdentityCandidate(context.Context, string, ViewContext) ([]Skill, error)
	LoadCurrentRevision(context.Context, Skill) (ManagedRevision, error)
	LoadExactRevision(context.Context, Skill, string) (ManagedRevision, error)
}

// listManagedIdentitiesWhenAvailable removes only the unavailable managed
// authority from a merged Skill view. Project and release-builtin Skills remain
// usable while startup reconciliation is pending or degraded.
func listManagedIdentitiesWhenAvailable(ctx context.Context, reader IdentityReader, view ViewContext) ([]Skill, error) {
	identities, err := reader.ListIdentityVisible(ctx, view)
	if errors.Is(err, ErrManagedSkillsUnavailable) {
		return nil, nil
	}
	return identities, err
}

// RuntimeReader is the complete managed-Skill read boundary used by an Agent
// turn. Runtime usage is pinned to the exact verified revision that was loaded;
// an identity-only or digest-free implementation cannot serve executable Skill
// content.
type RuntimeReader interface {
	IdentityReader
	TouchReflectSkillRuntimeUseDigest(context.Context, string, string, string, string) error
}

// IsCurrentSelectorMissing reports the narrow recoverable catalog state where
// the identity still exists but its Home current-selector entry is absent.
func IsCurrentSelectorMissing(err error) bool {
	return errors.Is(err, errCurrentSkillSelectorMissing)
}

// ManagedDeleter is the digest-CAS delete surface used by authorized internal
// transports. Ambient plugin mutations remain unavailable.
type ManagedDeleter interface {
	DeleteManagedSkill(context.Context, ManagedSkillDelete) error
	DeleteManagedSkillFile(context.Context, ManagedSkillFileDelete) (SkillSnapshot, error)
}
