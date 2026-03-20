package channel

import (
	"context"
	"fmt"
	"strings"

	"github.com/vaayne/anna/internal/auth"
	"github.com/vaayne/anna/internal/config"
)

// AgentCommander handles the /agent slash command for listing and switching agents.
type AgentCommander struct {
	store     config.Store
	authStore auth.AuthStore
}

// NewAgentCommander creates a new AgentCommander backed by the given stores.
func NewAgentCommander(store config.Store, authStore auth.AuthStore) *AgentCommander {
	return &AgentCommander{store: store, authStore: authStore}
}

// List returns all enabled agents.
func (ac *AgentCommander) List(ctx context.Context) ([]config.Agent, error) {
	return ac.store.ListEnabledAgents(ctx)
}

// Switch sets the active agent for a DM (updates user's default_agent_id)
// or a group chat (updates chat_agents). Returns an error if the agent slug
// is not found or not enabled.
func (ac *AgentCommander) Switch(ctx context.Context, user auth.AuthUser, chat ChatContext, agentSlug string) error {
	agentSlug = strings.TrimSpace(agentSlug)
	if agentSlug == "" {
		return fmt.Errorf("agent slug is required")
	}

	// Verify the agent exists and is enabled.
	ag, err := ac.store.GetAgent(ctx, agentSlug)
	if err != nil {
		return fmt.Errorf("agent %q not found", agentSlug)
	}
	if !ag.Enabled {
		return fmt.Errorf("agent %q is not enabled", agentSlug)
	}

	if chat.IsGroup && chat.ChatID != "" {
		// Group: update chat_agents mapping.
		return ac.store.SetChatAgent(ctx, chat.Platform, chat.ChatID, ag.ID)
	}

	// DM: update user's default agent.
	if user.ID == 0 {
		return fmt.Errorf("link your account first to set a default agent")
	}
	return ac.authStore.UpdateUserDefaultAgent(ctx, user.ID, ag.ID)
}

// FormatAgentList formats the agent list for display, marking the current agent.
func FormatAgentList(agents []config.Agent, currentAgentID string) string {
	if len(agents) == 0 {
		return "No agents available."
	}

	var b strings.Builder
	b.WriteString("Available agents:\n")
	for _, ag := range agents {
		marker := "  "
		if ag.ID == currentAgentID {
			marker = "> "
		}
		fmt.Fprintf(&b, "%s%s (%s)\n", marker, ag.ID, ag.Name)
	}
	b.WriteString("\nUse /agent <name> to switch.")
	return b.String()
}
