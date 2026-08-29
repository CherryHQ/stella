package skills

import (
	"context"
	"fmt"

	"github.com/CherryHQ/stella/internal/authz"
)

// ManagementAccess is the narrow Skill PEP port shared by HTTP and Stella
// management tools. Scope and owner identity are resolved from Authority, never
// supplied by a model argument.
type ManagementAccess interface {
	ManageScope(context.Context, authz.Authority, string, string) (userID, agentID string, err error)
	ManageByID(context.Context, authz.Authority, string, authz.Action) (Skill, error)
}

// ManagementStore is the managed-Skill persistence and Home lifecycle surface.
type ManagementStore interface {
	IdentityReader
	CreateManagedSkill(context.Context, Skill, map[string]string) (SkillSnapshot, error)
	UpdateManagedSkill(context.Context, ManagedSkillUpdate) (SkillSnapshot, error)
	DeleteManagedSkill(context.Context, ManagedSkillDelete) error
}

// Management owns the application-level CRUD orchestration for managed Skills.
// Install and multipart upload remain HTTP-only because they accept external or
// unbounded sources; the model path accepts one bounded sandbox content_path.
type Management struct {
	store  ManagementStore
	access ManagementAccess
}

func NewManagement(store ManagementStore, access ManagementAccess) *Management {
	return &Management{store: store, access: access}
}

func (m *Management) List(ctx context.Context, authority authz.Authority, scope, targetAgentID string) ([]ManagedRevision, error) {
	userID, agentID, err := m.manageScope(ctx, authority, scope, targetAgentID)
	if err != nil {
		return nil, err
	}
	identities, err := m.store.ListIdentityByScope(ctx, scope, userID, agentID)
	if err != nil {
		return nil, err
	}
	out := make([]ManagedRevision, 0, len(identities))
	for _, identity := range identities {
		revision, err := m.store.LoadCurrentRevision(ctx, identity)
		if err != nil {
			return nil, err
		}
		out = append(out, revision)
	}
	return out, nil
}

func (m *Management) Create(ctx context.Context, authority authz.Authority, in ManagedCreate) (SkillSnapshot, error) {
	userID, agentID, err := m.manageScope(ctx, authority, in.Scope, in.TargetAgentID)
	if err != nil {
		return SkillSnapshot{}, err
	}
	if in.Name == "" {
		return SkillSnapshot{}, fmt.Errorf("skill name is required")
	}
	if in.Files == nil || in.Files[MainFile] == "" {
		return SkillSnapshot{}, fmt.Errorf("files must include %s", MainFile)
	}
	return m.store.CreateManagedSkill(ctx, Skill{
		Scope: in.Scope, UserID: userID, AgentID: agentID, Name: in.Name,
		Description: in.Description, DisableModelInvocation: in.DisableModelInvocation,
		Status: SkillStatusActive,
	}, in.Files)
}

func (m *Management) Get(ctx context.Context, authority authz.Authority, id string) (ManagedRevision, error) {
	skill, err := m.manageByID(ctx, authority, id, authz.ActionRead)
	if err != nil {
		return ManagedRevision{}, err
	}
	return m.store.LoadCurrentRevision(ctx, skill)
}

func (m *Management) Update(ctx context.Context, authority authz.Authority, in ManagedUpdate) (SkillSnapshot, error) {
	identity, err := m.manageByID(ctx, authority, in.ID, authz.ActionWrite)
	if err != nil {
		return SkillSnapshot{}, err
	}
	current, err := m.store.LoadCurrentRevision(ctx, identity)
	if err != nil {
		return SkillSnapshot{}, err
	}
	if in.ExpectedVersion == "" || in.ExpectedVersion != current.Skill.ContentDigest {
		return SkillSnapshot{}, ErrSkillDigestConflict
	}
	return m.store.UpdateManagedSkill(ctx, ManagedSkillUpdate{
		ID: current.Skill.ID, UserID: current.Skill.UserID, AgentID: current.Skill.AgentID, Scope: current.Skill.Scope,
		Patch: in.Patch, Files: in.Files, ConvertToManual: in.ConvertToManual,
		ExpectedDigest: in.ExpectedVersion,
	})
}

func (m *Management) Delete(ctx context.Context, authority authz.Authority, id, expectedVersion string) error {
	identity, err := m.manageByID(ctx, authority, id, authz.ActionDelete)
	if err != nil {
		return err
	}
	current, err := m.store.LoadCurrentRevision(ctx, identity)
	if err != nil {
		return err
	}
	if expectedVersion == "" || expectedVersion != current.Skill.ContentDigest {
		return ErrSkillDigestConflict
	}
	return m.store.DeleteManagedSkill(ctx, ManagedSkillDelete{
		ID: current.Skill.ID, UserID: current.Skill.UserID, AgentID: current.Skill.AgentID, Scope: current.Skill.Scope,
		ExpectedDigest: expectedVersion,
	})
}

func (m *Management) manageScope(ctx context.Context, authority authz.Authority, scope, agentID string) (string, string, error) {
	if m == nil || m.store == nil || m.access == nil {
		return "", "", ErrManagedSkillsUnavailable
	}
	return m.access.ManageScope(ctx, authority, scope, agentID)
}

func (m *Management) manageByID(ctx context.Context, authority authz.Authority, id string, action authz.Action) (Skill, error) {
	if m == nil || m.store == nil || m.access == nil {
		return Skill{}, ErrManagedSkillsUnavailable
	}
	return m.access.ManageByID(ctx, authority, id, action)
}

type ManagedCreate struct {
	Scope                  string
	TargetAgentID          string
	Name                   string
	Description            string
	DisableModelInvocation bool
	Files                  map[string]string
}

type ManagedUpdate struct {
	ID              string
	ExpectedVersion string
	Patch           UpdatePatch
	Files           map[string]string
	ConvertToManual bool
}
