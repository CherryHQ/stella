package access

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/agent/prompt"
	agentsession "github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

// SystemPromptInput is the transport-owned identity and route tuple for the
// session system-prompt read use case.
type SystemPromptInput struct {
	Authority authz.Authority
	AgentID   string
	SessionID string
}

// PromptAgent is the narrow agent config needed to build a system prompt.
type PromptAgent struct {
	SystemPrompt string
	Workspace    string
}

type PromptAgentStore interface {
	GetPromptAgent(context.Context, string) (PromptAgent, error)
}

type PromptProjectStore interface {
	ProjectRoot(context.Context, string, string) (string, error)
}

type PromptWorkspace interface {
	SetupPrincipalWorkspace(stellaHome, userID, groupID, agentID string) (homeDir, agentDir string, err error)
}

type PromptPlugins interface {
	SessionPluginView(context.Context) (pkgplugins.SessionPluginView, error)
	SystemPromptSections(context.Context, pkgplugins.SystemPromptContext) ([]pkgplugins.SystemPromptSection, error)
	ManifestPluginPrompts() []pkgplugins.SystemPromptSection
}

type PromptSkillSectionBuilder func(context.Context, pkgplugins.SystemPromptContext) (pkgplugins.SystemPromptSection, error)

type SystemPromptBuilder interface {
	BuildSessionSystemPrompt(context.Context, SystemPromptBuildInput) (string, error)
}

type SystemPromptBuildInput struct {
	Info agentsession.Info
}

type SystemPromptDeps struct {
	StellaHome string
	HomeDir    string
	Memory     memory.Provider
	Agents     PromptAgentStore
	Projects   PromptProjectStore
	Workspace  PromptWorkspace
	Plugins    PromptPlugins
	SkillStore pkgplugins.SkillStore
	Skills     PromptSkillSectionBuilder
}

type defaultSystemPromptBuilder struct {
	deps SystemPromptDeps
}

func NewSystemPromptBuilder(deps SystemPromptDeps) (SystemPromptBuilder, error) {
	missing := ""
	if deps.StellaHome == "" {
		missing = "StellaHome"
	}
	if deps.Memory == nil {
		missing = appendMissing(missing, "Memory")
	}
	if deps.Agents == nil {
		missing = appendMissing(missing, "Agents")
	}
	if deps.Projects == nil {
		missing = appendMissing(missing, "Projects")
	}
	if deps.Workspace == nil {
		missing = appendMissing(missing, "Workspace")
	}
	if deps.Plugins == nil {
		missing = appendMissing(missing, "Plugins")
	}
	if deps.SkillStore == nil {
		missing = appendMissing(missing, "SkillStore")
	}
	if deps.Skills == nil {
		missing = appendMissing(missing, "Skills")
	}
	if missing != "" {
		return nil, fmt.Errorf("session prompt builder: missing %s", missing)
	}
	return &defaultSystemPromptBuilder{deps: deps}, nil
}

func appendMissing(current, next string) string {
	if current == "" {
		return next
	}
	return current + ", " + next
}

func (b *defaultSystemPromptBuilder) BuildSessionSystemPrompt(ctx context.Context, in SystemPromptBuildInput) (string, error) {
	info := in.Info
	var agentCfg PromptAgent
	if info.AgentID != "" {
		agentCfg, _ = b.deps.Agents.GetPromptAgent(ctx, info.AgentID)
	}

	userRoot := ""
	workspaceRoot := agentCfg.Workspace
	if (info.UserID != "" || info.GroupID != "") && info.AgentID != "" {
		if homeDir, agentDir, err := b.deps.Workspace.SetupPrincipalWorkspace(b.deps.StellaHome, info.UserID, info.GroupID, info.AgentID); err == nil {
			userRoot = homeDir
			workspaceRoot = agentDir
		}
	}

	projectRoot := ""
	if info.UserID != "" && info.ProjectID != "" {
		root, err := b.deps.Projects.ProjectRoot(ctx, info.UserID, info.ProjectID)
		if err != nil {
			return "", fmt.Errorf("%w: project root: %w", ErrUnavailable, err)
		}
		projectRoot = root
	}

	pluginView, err := b.deps.Plugins.SessionPluginView(ctx)
	if err != nil {
		return "", fmt.Errorf("%w: session plugin view: %w", ErrUnavailable, err)
	}
	promptBuild := pkgplugins.SystemPromptContext{
		StellaHome:          b.deps.StellaHome,
		HomeDir:             b.deps.HomeDir,
		AgentRoot:           agentCfg.Workspace,
		ProjectRoot:         projectRoot,
		UserID:              info.UserID,
		AgentID:             info.AgentID,
		UserRoot:            userRoot,
		WorkspaceRoot:       workspaceRoot,
		SkillStore:          b.deps.SkillStore,
		RegisteredPluginIDs: pluginView.RegisteredPluginIDs,
		EnabledPluginIDs:    pluginView.EnabledPluginIDs,
	}
	promptSections, err := b.deps.Plugins.SystemPromptSections(ctx, promptBuild)
	if err != nil {
		return "", fmt.Errorf("%w: system prompt sections: %w", ErrUnavailable, err)
	}
	if skillsSection, err := b.deps.Skills(ctx, promptBuild); err != nil {
		return "", fmt.Errorf("%w: skills prompt section: %w", ErrUnavailable, err)
	} else if skillsSection.Title != "" && skillsSection.Content != "" {
		promptSections = append(promptSections, skillsSection)
	}

	promptUserID := info.UserID
	if info.GroupID != "" {
		promptUserID = ""
	}
	return prompt.BuildSystemPromptFromDB(ctx, prompt.DBPromptParams{
		SystemPrompt: agentCfg.SystemPrompt,
		Memory:       b.deps.Memory,
		UserID:       promptUserID,
		AgentID:      info.AgentID,
		GroupID:      info.GroupID,
		StellaHome:   b.deps.StellaHome,
		AgentRoot:    agentCfg.Workspace,
		UserRoot:     userRoot,
		Sections:     append(promptSections, b.deps.Plugins.ManifestPluginPrompts()...),
	}), nil
}

// ConfigPromptAgentStore adapts the deployment config store to the prompt port.
type ConfigPromptAgentStore struct{ Store config.Store }

func (s ConfigPromptAgentStore) GetPromptAgent(ctx context.Context, agentID string) (PromptAgent, error) {
	agentCfg, err := s.Store.GetAgent(ctx, agentID)
	if err != nil {
		return PromptAgent{}, err
	}
	return PromptAgent{SystemPrompt: agentCfg.SystemPrompt, Workspace: agentCfg.Workspace}, nil
}

// SQLPromptProjectStore adapts project persistence to a prompt-only root lookup.
type SQLPromptProjectStore struct{ q *sqlc.Queries }

func NewSQLPromptProjectStore(db sqlc.DBTX) *SQLPromptProjectStore {
	return &SQLPromptProjectStore{q: sqlc.New(db)}
}

func (s *SQLPromptProjectStore) ProjectRoot(ctx context.Context, userID, projectID string) (string, error) {
	p, err := s.q.GetProject(ctx, sqlc.GetProjectParams{ID: projectID, UserID: userID})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return p.BaseDir, nil
}

// AgentPromptWorkspace adapts the agent workspace materializer to the prompt port.
type AgentPromptWorkspace struct{}

func (AgentPromptWorkspace) SetupPrincipalWorkspace(stellaHome, userID, groupID, agentID string) (string, string, error) {
	workspace, err := agent.SetupPrincipalWorkspace(stellaHome, userID, groupID, agentID)
	return workspace.HomeDir, workspace.AgentDir, err
}
