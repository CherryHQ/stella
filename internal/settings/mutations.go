package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/CherryHQ/stella/internal/agent"
	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/library"
	"github.com/CherryHQ/stella/internal/skillaccess"
	"github.com/CherryHQ/stella/internal/skills"
)

// AgentMutation is the narrow write port used by the settings facade. The
// concrete Management service owns the Agent PEP and durable invariants.
type AgentMutation interface {
	Create(context.Context, authz.Authority, config.Agent) (config.Agent, error)
	Update(context.Context, authz.Authority, config.Agent) (config.Agent, error)
	Delete(context.Context, authz.Authority, string) error
}

type LibraryMutation interface {
	ResolveManageOwner(context.Context, authz.Authority, library.Scope, string) (library.Owner, error)
	GetManaged(context.Context, authz.Authority, string) (library.LibraryFile, error)
	CreateManagedUpload(context.Context, authz.Authority, library.Scope, string, string, io.Reader) (library.LibraryFile, error)
	DeleteManagedWithDigest(context.Context, authz.Authority, string, string) error
}

type SkillMutation interface {
	PreviewCreate(context.Context, authz.Authority, string, string) error
	PreviewExisting(context.Context, authz.Authority, string, authz.Action) (skills.Skill, error)
	Create(context.Context, authz.Authority, skillCreateRequest) (skills.SkillSnapshot, error)
	Update(context.Context, authz.Authority, skillUpdateRequest) (skills.SkillSnapshot, error)
	Delete(context.Context, authz.Authority, skillDeleteRequest) error
}

type ToolOverrideMutation interface {
	Preview(context.Context, authz.Authority, toolOverrideRequest) (string, error)
	Set(context.Context, authz.Authority, toolOverrideRequest, string) error
	Clear(context.Context, authz.Authority, toolOverrideRequest, string) error
}

type SkillStore interface {
	CreateManagedSkill(context.Context, skills.Skill, map[string]string) (skills.SkillSnapshot, error)
	UpdateManagedSkill(context.Context, skills.ManagedSkillUpdate) (skills.SkillSnapshot, error)
	DeleteManagedSkill(context.Context, skills.ManagedSkillDelete) error
}

type skillMutator struct {
	access *skillaccess.Service
	store  SkillStore
}

func NewSkillMutator(access *skillaccess.Service, store SkillStore) SkillMutation {
	return &skillMutator{access: access, store: store}
}

func (m *skillMutator) PreviewCreate(ctx context.Context, authority authz.Authority, scope, agentID string) error {
	if m == nil || m.access == nil || m.store == nil {
		return skills.ErrManagedSkillsUnavailable
	}
	acc, err := m.access.Begin(ctx, authority)
	if err != nil {
		return err
	}
	_, _, err = acc.AuthorizeManageScope(ctx, scope, agentID)
	return err
}

func (m *skillMutator) PreviewExisting(ctx context.Context, authority authz.Authority, id string, action authz.Action) (skills.Skill, error) {
	if m == nil || m.access == nil || m.store == nil {
		return skills.Skill{}, skills.ErrManagedSkillsUnavailable
	}
	acc, err := m.access.Begin(ctx, authority)
	if err != nil {
		return skills.Skill{}, err
	}
	return acc.AuthorizeManageByID(ctx, id, action)
}

func (m *skillMutator) Create(ctx context.Context, authority authz.Authority, in skillCreateRequest) (skills.SkillSnapshot, error) {
	if err := m.PreviewCreate(ctx, authority, in.Scope, in.AgentID); err != nil {
		return skills.SkillSnapshot{}, err
	}
	acc, err := m.access.Begin(ctx, authority)
	if err != nil {
		return skills.SkillSnapshot{}, err
	}
	userID, agentID, err := acc.AuthorizeManageScope(ctx, in.Scope, in.AgentID)
	if err != nil {
		return skills.SkillSnapshot{}, err
	}
	if in.Files == nil {
		in.Files = map[string]string{skills.MainFile: skillFileContent(in.Name, in.Description, in.Body)}
	}
	if _, ok := in.Files[skills.MainFile]; !ok {
		return skills.SkillSnapshot{}, fmt.Errorf("files must include %s", skills.MainFile)
	}
	return m.store.CreateManagedSkill(ctx, skills.Skill{
		Scope: in.Scope, UserID: userID, AgentID: agentID,
		Name: in.Name, Description: in.Description,
		DisableModelInvocation: in.DisableModelInvocation,
	}, in.Files)
}

func (m *skillMutator) Update(ctx context.Context, authority authz.Authority, in skillUpdateRequest) (skills.SkillSnapshot, error) {
	sk, err := m.PreviewExisting(ctx, authority, in.ID, authz.ActionWrite)
	if err != nil {
		return skills.SkillSnapshot{}, err
	}
	if in.ExpectedDigest == "" || sk.ContentDigest != in.ExpectedDigest {
		return skills.SkillSnapshot{}, skills.ErrSkillDigestConflict
	}
	patch := skills.UpdatePatch{Description: in.Description, DisableModelInvocation: in.DisableModelInvocation}
	return m.store.UpdateManagedSkill(ctx, skills.ManagedSkillUpdate{
		ID: sk.ID, UserID: sk.UserID, AgentID: sk.AgentID, Scope: sk.Scope,
		Patch: patch, Files: in.Files, DeleteFiles: in.DeleteFiles,
		ExpectedDigest: in.ExpectedDigest,
	})
}

func (m *skillMutator) Delete(ctx context.Context, authority authz.Authority, in skillDeleteRequest) error {
	sk, err := m.PreviewExisting(ctx, authority, in.ID, authz.ActionDelete)
	if err != nil {
		return err
	}
	if in.ExpectedDigest == "" || sk.ContentDigest != in.ExpectedDigest {
		return skills.ErrSkillDigestConflict
	}
	return m.store.DeleteManagedSkill(ctx, skills.ManagedSkillDelete{
		ID: sk.ID, UserID: sk.UserID, AgentID: sk.AgentID, Scope: sk.Scope,
		ExpectedDigest: in.ExpectedDigest,
	})
}

type ToolOverrideWriter interface {
	Get(context.Context, agent.ToolOverrideKey) (agent.ToolOverride, bool, error)
	Set(context.Context, agent.ToolOverrideWrite) error
	Clear(context.Context, agent.ToolOverrideKey) error
}

type RunnerInvalidator interface {
	InvalidateUser(string) error
	InvalidateUserAgent(string, string) error
	InvalidateAgent(string) error
	InvalidateAll() error
}

type ToolNameManager func(context.Context, string) bool

type toolOverrideMutator struct {
	access      *agentaccess.Service
	store       ToolOverrideWriter
	invalidator RunnerInvalidator
	isManaged   ToolNameManager
}

func NewToolOverrideMutator(access *agentaccess.Service, store ToolOverrideWriter, invalidator RunnerInvalidator, isManaged ToolNameManager) ToolOverrideMutation {
	return &toolOverrideMutator{access: access, store: store, invalidator: invalidator, isManaged: isManaged}
}

func (m *toolOverrideMutator) Preview(ctx context.Context, authority authz.Authority, in toolOverrideRequest) (string, error) {
	if m == nil || m.access == nil || m.store == nil || m.invalidator == nil {
		return "", ErrUnavailable
	}
	if strings.TrimSpace(in.ToolName) == "" || agent.IsCoreToolName(in.ToolName) || m.isManaged == nil || !m.isManaged(ctx, in.ToolName) {
		return "", fmt.Errorf("tool override is not managed")
	}
	if !authority.Valid() {
		return "", agentaccess.ErrForbidden
	}
	switch in.Scope {
	case agent.ToolOverrideScopeSystem, agent.ToolOverrideScopeSystemAgent:
		if !authority.IsAdmin() {
			return "", agentaccess.ErrForbidden
		}
	case agent.ToolOverrideScopeUser, agent.ToolOverrideScopeUserAgent:
		if authority.Kind() != authz.ActorUser {
			return "", agentaccess.ErrForbidden
		}
	default:
		return "", fmt.Errorf("invalid tool override scope %q", in.Scope)
	}
	if in.Scope == agent.ToolOverrideScopeUserAgent || in.Scope == agent.ToolOverrideScopeSystemAgent {
		if in.AgentID == "" {
			return "", fmt.Errorf("agent_id is required for %s scope", in.Scope)
		}
		if _, err := m.access.Read(ctx, authority, in.AgentID); err != nil {
			return "", err
		}
	} else if in.AgentID != "" {
		return "", fmt.Errorf("agent_id is not allowed for %s scope", in.Scope)
	}
	userID, agentID := overrideOwner(authority, in)
	current, found, err := m.store.Get(ctx, agent.ToolOverrideKey{ToolName: in.ToolName, Scope: in.Scope, UserID: userID, AgentID: agentID})
	if err != nil {
		return "", err
	}
	if !found {
		return digestValue(map[string]any{"found": false}), nil
	}
	return digestValue(map[string]any{"found": true, "enabled": current.Enabled}), nil
}

func (m *toolOverrideMutator) checkCurrent(ctx context.Context, authority authz.Authority, in toolOverrideRequest, expected string) error {
	current, err := m.Preview(ctx, authority, in)
	if err != nil {
		return err
	}
	if expected == "" || current != expected {
		return errors.New("tool override changed since preview")
	}
	return nil
}

func (m *toolOverrideMutator) Set(ctx context.Context, authority authz.Authority, in toolOverrideRequest, expected string) error {
	if err := m.checkCurrent(ctx, authority, in, expected); err != nil {
		return err
	}
	userID, agentID := overrideOwner(authority, in)
	if err := m.store.Set(ctx, agent.ToolOverrideWrite{ToolName: in.ToolName, Scope: in.Scope, UserID: userID, AgentID: agentID, Enabled: in.Enabled}); err != nil {
		return err
	}
	return m.invalidate(in.Scope, userID, agentID)
}

func (m *toolOverrideMutator) Clear(ctx context.Context, authority authz.Authority, in toolOverrideRequest, expected string) error {
	if err := m.checkCurrent(ctx, authority, in, expected); err != nil {
		return err
	}
	userID, agentID := overrideOwner(authority, in)
	if err := m.store.Clear(ctx, agent.ToolOverrideKey{ToolName: in.ToolName, Scope: in.Scope, UserID: userID, AgentID: agentID}); err != nil {
		return err
	}
	return m.invalidate(in.Scope, userID, agentID)
}

func (m *toolOverrideMutator) invalidate(scope, userID, agentID string) error {
	switch scope {
	case agent.ToolOverrideScopeUser:
		return m.invalidator.InvalidateUser(userID)
	case agent.ToolOverrideScopeUserAgent:
		return m.invalidator.InvalidateUserAgent(userID, agentID)
	case agent.ToolOverrideScopeSystemAgent:
		return m.invalidator.InvalidateAgent(agentID)
	case agent.ToolOverrideScopeSystem:
		return m.invalidator.InvalidateAll()
	default:
		return agentaccess.ErrForbidden
	}
}

func overrideOwner(authority authz.Authority, in toolOverrideRequest) (string, string) {
	if in.Scope == agent.ToolOverrideScopeUser || in.Scope == agent.ToolOverrideScopeUserAgent {
		return string(authority.UserID()), in.AgentID
	}
	return "", in.AgentID
}

type skillCreateRequest struct {
	Scope                  string
	AgentID                string
	Name                   string
	Description            string
	Body                   string
	Files                  map[string]string
	DisableModelInvocation bool
}

type skillUpdateRequest struct {
	ID                     string
	ExpectedDigest         string
	Description            *string
	DisableModelInvocation *bool
	Files                  map[string]string
	DeleteFiles            []string
}

type skillDeleteRequest struct{ ID, ExpectedDigest string }

type toolOverrideRequest struct {
	ToolName string
	Scope    string
	AgentID  string
	Enabled  bool
}

func decodeObject(value any, out any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return fmt.Errorf("invalid mutation input: %w", err)
	}
	return nil
}

func skillFileContent(name, description, body string) string {
	var b strings.Builder
	b.WriteString("---\nname: ")
	b.WriteString(name)
	b.WriteString("\ndescription: ")
	b.WriteString(description)
	b.WriteString("\n---\n")
	b.WriteString(body)
	if body != "" && !strings.HasSuffix(body, "\n") {
		b.WriteByte('\n')
	}
	return b.String()
}

func ensureSkillInput(in skillCreateRequest) error {
	if strings.TrimSpace(in.Scope) == "" || strings.TrimSpace(in.Name) == "" {
		return errors.New("skill scope and name are required")
	}
	if in.Scope != skillaccess.ScopeUser && in.Scope != skillaccess.ScopeUserAgent && in.Scope != skillaccess.ScopeSystem && in.Scope != skillaccess.ScopeSystemAgent {
		return fmt.Errorf("invalid skill scope %q", in.Scope)
	}
	if in.Scope == skillaccess.ScopeUserAgent || in.Scope == skillaccess.ScopeSystemAgent {
		if strings.TrimSpace(in.AgentID) == "" {
			return errors.New("agent_id is required for agent-bound skills")
		}
	} else if in.AgentID != "" {
		return errors.New("agent_id is not allowed for this skill scope")
	}
	if len(in.Files) == 0 && strings.TrimSpace(in.Body) == "" {
		return errors.New("skill body or files are required")
	}
	return nil
}
