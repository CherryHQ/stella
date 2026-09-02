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

// AgentSystemPrompt reads one agent's configured system prompt. It is a func
// rather than a port because the builder's other config-shaped deps (Projects,
// Skills) already are, and because a failure here is non-fatal: the prompt is
// simply built without the agent's own instructions.
type AgentSystemPrompt func(ctx context.Context, agentID string) (string, error)

// ConfigAgentSystemPrompt reads the system prompt from the deployment config store.
func ConfigAgentSystemPrompt(store config.Store) AgentSystemPrompt {
	return func(ctx context.Context, agentID string) (string, error) {
		agentCfg, err := store.GetAgent(ctx, agentID)
		if err != nil {
			return "", err
		}
		return agentCfg.SystemPrompt, nil
	}
}

type PromptPlugins interface {
	SessionPluginView(context.Context) (pkgplugins.SessionPluginView, error)
	SystemPromptSections(context.Context, pkgplugins.SystemPromptContext) ([]pkgplugins.SystemPromptSection, error)
	ManifestPluginPrompts() []pkgplugins.SystemPromptSection
}

type PromptSkillSectionBuilder func(context.Context, pkgplugins.SystemPromptContext, *skill.ProjectSnapshot) (pkgplugins.SystemPromptSection, error)

type SystemPromptBuildInput struct {
	Info agentsession.Info
}

type SystemPromptDeps struct {
	Memory    memory.Provider
	Agents    AgentSystemPrompt
	Projects  agent.ProjectResolverFunc
	Workspace home.RootOpener
	Plugins   PromptPlugins
	Skills    PromptSkillSectionBuilder
}

// SystemPromptBuilder assembles a session's effective system prompt from the
// deps it was constructed with. Its deps are the seams; the builder itself has
// exactly one behaviour.
type SystemPromptBuilder struct {
	deps SystemPromptDeps
}

func NewSystemPromptBuilder(deps SystemPromptDeps) (*SystemPromptBuilder, error) {
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
	return &SystemPromptBuilder{deps: deps}, nil
}

func appendMissing(current, next string) string {
	if current == "" {
		return next
	}
	return current + ", " + next
}

func (b *SystemPromptBuilder) BuildSessionSystemPrompt(ctx context.Context, in SystemPromptBuildInput) (string, error) {
	info := in.Info
	ctx = authz.WithAgentID(ctx, info.AgentID)
	if info.GroupID != "" {
		ctx = authz.WithGroupID(ctx, info.GroupID)
	} else if info.UserID != "" {
		ctx = authz.WithUserID(ctx, info.UserID)
	}
	var agentPrompt string
	if info.AgentID != "" {
		agentPrompt, _ = b.deps.Agents(ctx, info.AgentID)
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
		SystemPrompt:   agentPrompt,
		Memory:         b.deps.Memory,
		UserID:         promptUserID,
		AgentID:        info.AgentID,
		GroupID:        info.GroupID,
		ProjectContext: projectContext,
		Sections:       append(promptSections, b.deps.Plugins.ManifestPluginPrompts()...),
	})
	return system, nil
}
