package channel

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/vaayne/anna/internal/agent"
	"github.com/vaayne/anna/internal/agent/runner"
	"github.com/vaayne/anna/internal/auth"
	"github.com/vaayne/anna/internal/config"
	pkgchannel "github.com/vaayne/anna/pkg/channel"
)

// Coordinator implements pkgchannel.MessageHandler. It owns all business logic
// that channels previously called directly: user/agent resolution, session
// management, command handling, account linking, and model/agent switching.
type Coordinator struct {
	poolManager *agent.PoolManager
	store       config.Store
	authStore   auth.AuthStore
	engine      *auth.PolicyEngine
	linkCodes   *auth.LinkCodeStore
	listFn      func() []pkgchannel.ModelOption
	switchFn    func(provider, model string) error
}

// CoordinatorOption configures the Coordinator.
type CoordinatorOption func(*Coordinator)

// WithCoordinatorAuth configures the coordinator with auth support.
func WithCoordinatorAuth(authStore auth.AuthStore, engine *auth.PolicyEngine, linkCodes *auth.LinkCodeStore) CoordinatorOption {
	return func(c *Coordinator) {
		c.authStore = authStore
		c.engine = engine
		c.linkCodes = linkCodes
	}
}

// NewCoordinator creates a Coordinator that satisfies pkgchannel.MessageHandler.
func NewCoordinator(
	pm *agent.PoolManager,
	store config.Store,
	listFn func() []pkgchannel.ModelOption,
	switchFn func(provider, model string) error,
	opts ...CoordinatorOption,
) *Coordinator {
	c := &Coordinator{
		poolManager: pm,
		store:       store,
		listFn:      listFn,
		switchFn:    switchFn,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// resolve performs the full user -> agent -> pool -> session key resolution.
func (c *Coordinator) resolve(ctx context.Context, msg pkgchannel.IncomingMessage) (*ResolvedChat, error) {
	return Resolve(ctx, c.poolManager, c.store, c.authStore, c.engine, msg.Platform, msg.SenderID, msg.SenderName, msg.ChatID, msg.IsGroup)
}

// HandleMessage resolves the user, routes to an agent, and streams a response.
func (c *Coordinator) HandleMessage(ctx context.Context, msg pkgchannel.IncomingMessage) (*pkgchannel.ChatStream, error) {
	rc, err := c.resolve(ctx, msg)
	if err != nil {
		return nil, err
	}

	events, sessionID, err := rc.Chat(ctx, msg.Content)
	if err != nil {
		return nil, err
	}

	// Convert internal runner.Event to pkg/channel.Event.
	out := make(chan pkgchannel.Event, 100)
	go func() {
		defer close(out)
		for evt := range events {
			out <- convertEvent(evt)
		}
	}()

	return &pkgchannel.ChatStream{
		Events:    out,
		SessionID: sessionID,
	}, nil
}

// HandleCommand processes shared commands (/start, /new, /compact, /whoami, /link).
func (c *Coordinator) HandleCommand(ctx context.Context, msg pkgchannel.IncomingMessage, command, args string) (string, bool) {
	// Try link code first (before auth resolution, since it creates identity).
	if c.authStore != nil && c.linkCodes != nil {
		fullText := command
		if args != "" {
			fullText = command + " " + args
		}
		if resp, ok := TryLinkCode(ctx, c.authStore, c.linkCodes, fullText, msg.Platform, msg.SenderID, msg.SenderName); ok {
			return resp, true
		}
	}

	rc, err := c.resolve(ctx, msg)
	if err != nil {
		return fmt.Sprintf("Error: %v", err), true
	}

	return HandleCommand(ctx, rc, command+" "+args, msg.SenderID)
}

// ListAgents returns enabled agents the user can access and the current agent ID.
func (c *Coordinator) ListAgents(ctx context.Context, msg pkgchannel.IncomingMessage) ([]pkgchannel.AgentInfo, string, error) {
	rc, err := c.resolve(ctx, msg)
	if err != nil {
		return nil, "", err
	}

	ac := NewAgentCommander(c.store, c.authStore)
	agents, err := ac.List(ctx)
	if err != nil {
		return nil, "", err
	}

	infos := make([]pkgchannel.AgentInfo, len(agents))
	for i, a := range agents {
		infos[i] = pkgchannel.AgentInfo{ID: a.ID, Name: a.Name}
	}
	return infos, rc.AgentID, nil
}

// SwitchAgent switches the active agent for the chat context.
func (c *Coordinator) SwitchAgent(ctx context.Context, msg pkgchannel.IncomingMessage, agentSlug string) error {
	rc, err := c.resolve(ctx, msg)
	if err != nil {
		return err
	}

	ac := NewAgentCommander(c.store, c.authStore)
	return ac.Switch(ctx, rc.User, rc.ChatCtx, agentSlug)
}

// ListModels returns available models.
func (c *Coordinator) ListModels() []pkgchannel.ModelOption {
	return c.listFn()
}

// SwitchModel switches the active model.
func (c *Coordinator) SwitchModel(provider, model string) error {
	return c.switchFn(provider, model)
}

// convertEvent converts an internal runner.Event to a pkg/channel.Event.
func convertEvent(evt runner.Event) pkgchannel.Event {
	out := pkgchannel.Event{
		Text: evt.Text,
		Err:  evt.Err,
	}
	if evt.Image != nil {
		out.Image = &pkgchannel.ImageEvent{
			Data:     evt.Image.Data,
			MimeType: evt.Image.MimeType,
		}
	}
	if evt.ToolUse != nil {
		out.ToolUse = &pkgchannel.ToolUseEvent{
			Tool:   evt.ToolUse.Tool,
			Status: evt.ToolUse.Status,
			Input:  evt.ToolUse.Input,
			Detail: evt.ToolUse.Detail,
		}
	}
	return out
}

// compile-time check.
var _ pkgchannel.MessageHandler = (*Coordinator)(nil)

func init() {
	// Verify the logger is callable.
	_ = slog.Default()
}
