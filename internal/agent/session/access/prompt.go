package access

import (
	"context"
	"fmt"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/agent/prompt"
	agentsession "github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/platform/config"
	"github.com/CherryHQ/stella/internal/platform/home"
	"github.com/CherryHQ/stella/internal/skill"
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
}

type PromptAgentStore interface {
	GetPromptAgent(context.Context, string) (PromptAgent, error)
}

type PromptPlugins interface {
	SessionPluginView(context.Context) (pkgplugins.SessionPluginView, error)
	SystemPromptSections(context.Context, pkgplugins.SystemPromptContext) ([]pkgplugins.SystemPromptSection, error)
	ManifestPluginPrompts() []pkgplugins.SystemPromptSection
}

type PromptSkillSectionBuilder func(context.Context, pkgplugins.SystemPromptContext, *skill.ProjectSnapshot) (pkgplugins.SystemPromptSection, error)

type SystemPromptBuilder interface {
	BuildSessionSystemPrompt(context.Context, SystemPromptBuildInput) (string, error)
}

type SystemPromptBuildInput struct {
	Info agentsession.Info
}

type SystemPromptDeps struct {
	Memory    memory.Provider
	Agents    PromptAgentStore
	Projects  agent.ProjectResolverFunc
	Workspace home.RootOpener
	Plugins   PromptPlugins
	Skills    PromptSkillSectionBuilder
}

type defaultSystemPromptBuilder struct {
	deps SystemPromptDeps
}

func NewSystemPromptBuilder(deps SystemPromptDeps) (SystemPromptBuilder, error) {
	missing := ""
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
	ctx = authz.WithAgentID(ctx, info.AgentID)
	if info.GroupID != "" {
		ctx = authz.WithGroupID(ctx, info.GroupID)
	} else if info.UserID != "" {
		ctx = authz.WithUserID(ctx, info.UserID)
	}
	var agentCfg PromptAgent
	if info.AgentID != "" {
		agentCfg, _ = b.deps.Agents.GetPromptAgent(ctx, info.AgentID)
	}

	var projectContext prompt.ProjectContext
	var projectSkills *skill.ProjectSnapshot
	if info.UserID != "" && info.ProjectID != "" {
		projectSnapshot, err := agent.SnapshotAuthorizedProject(ctx, b.deps.Projects, b.deps.Workspace, info.ProjectID, info.UserID, info.AgentID)
		if err != nil {
			return "", fmt.Errorf("%w: project context: %w", ErrUnavailable, err)
		}
		projectContext, projectSkills = projectSnapshot.Context, projectSnapshot.Skills
	}

	pluginView, err := b.deps.Plugins.SessionPluginView(ctx)
	if err != nil {
		return "", fmt.Errorf("%w: session plugin view: %w", ErrUnavailable, err)
	}
	promptBuild := pkgplugins.SystemPromptContext{
		UserID:              info.UserID,
		AgentID:             info.AgentID,
		RegisteredPluginIDs: pluginView.RegisteredPluginIDs,
		EnabledPluginIDs:    pluginView.EnabledPluginIDs,
	}
	promptSections, err := b.deps.Plugins.SystemPromptSections(ctx, promptBuild)
	if err != nil {
		return "", fmt.Errorf("%w: system prompt sections: %w", ErrUnavailable, err)
	}
	if skillsSection, err := b.deps.Skills(ctx, promptBuild, projectSkills); err != nil {
		return "", fmt.Errorf("%w: skills prompt section: %w", ErrUnavailable, err)
	} else if skillsSection.Title != "" && skillsSection.Content != "" {
		promptSections = append(promptSections, skillsSection)
	}

	promptUserID := info.UserID
	if info.GroupID != "" {
		promptUserID = ""
	}
	system := prompt.BuildSystemPromptFromDB(ctx, prompt.DBPromptParams{
		SystemPrompt:   agentCfg.SystemPrompt,
		Memory:         b.deps.Memory,
		UserID:         promptUserID,
		AgentID:        info.AgentID,
		GroupID:        info.GroupID,
		ProjectContext: projectContext,
		Sections:       append(promptSections, b.deps.Plugins.ManifestPluginPrompts()...),
	})
	return system, nil
}

// ConfigPromptAgentStore adapts the deployment config store to the prompt port.
type ConfigPromptAgentStore struct{ Store config.Store }

func (s ConfigPromptAgentStore) GetPromptAgent(ctx context.Context, agentID string) (PromptAgent, error) {
	agentCfg, err := s.Store.GetAgent(ctx, agentID)
	if err != nil {
		return PromptAgent{}, err
	}
	return PromptAgent{SystemPrompt: agentCfg.SystemPrompt}, nil
}
