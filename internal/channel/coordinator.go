package channel

import (
	"context"

	"github.com/vaayne/anna/internal/agent"
	"github.com/vaayne/anna/internal/agent/runner"
	"github.com/vaayne/anna/internal/auth"
	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/pkg/ai"
	pkgchannel "github.com/vaayne/anna/pkg/channel"
)

// Coordinator implements pkgchannel.Handler. It owns all business logic
// that channels previously called directly: user/agent resolution, session
// management, command handling, account linking, and model/agent switching.
// A per-session message queue ensures that only one chat turn runs at a time
// per resolved Anna session; later messages are serialised in arrival order.
type Coordinator struct {
	poolManager *agent.PoolManager
	store       config.Store
	authStore   auth.AuthStore
	engine      *auth.PolicyEngine
	linkCodes   *auth.LinkCodeStore
	listFn      func() []pkgchannel.ModelOption
	switchFn    func(provider, model string) error
	queue       *sessionQueue
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

// NewCoordinator creates a Coordinator that satisfies pkgchannel.Handler.
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
		queue:       newSessionQueue(),
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// resolve performs the full user -> agent -> pool -> session key resolution.
func (c *Coordinator) resolve(ctx context.Context, msg pkgchannel.IncomingMessage) (*ResolvedChat, error) {
	channelID := msg.ChannelID
	if channelID == "" {
		channelID = msg.Platform
	}
	return ResolveWithChannel(ctx, c.poolManager, c.store, c.authStore, c.engine, msg.Platform, channelID, msg.SenderID, msg.SenderIDs, msg.SenderName, msg.ChatID, msg.IsGroup)
}

// HandleIncoming resolves the user once, tries command handling, and if the
// command is not handled, streams a chat response. This avoids double
// resolution when a plugin needs to try commands before messaging.
func (c *Coordinator) HandleIncoming(ctx context.Context, msg pkgchannel.IncomingMessage, command, args string) (string, bool, *pkgchannel.ChatStream, error) {
	// Try link code first (before auth resolution, since it creates identity).
	if c.authStore != nil && c.linkCodes != nil {
		fullText := command
		if args != "" {
			fullText = command + " " + args
		}
		if resp, ok := TryLinkCodeWithCandidates(ctx, c.authStore, c.linkCodes, fullText, msg.Platform, msg.SenderID, msg.SenderIDs, msg.SenderName); ok {
			return resp, true, nil, nil
		}
	}

	rc, err := c.resolve(ctx, msg)
	if err != nil {
		return "", false, nil, err
	}

	// Try shared commands.
	if command != "" {
		// /abort is handled here so it can reach the queue before resolution.
		if command == "/abort" {
			return c.handleAbort(rc), true, nil, nil
		}
		if resp, ok := HandleCommand(ctx, rc, command+" "+args, msg.SenderID); ok {
			return resp, true, nil, nil
		}
	}

	// Not a command — enqueue a chat response for this session.
	stream, err := c.queuedChat(ctx, rc, msg.Content)
	if err != nil {
		return "", false, nil, err
	}
	return "", false, stream, nil
}

// handleAbort cancels the currently-running request for the resolved session.
func (c *Coordinator) handleAbort(rc *ResolvedChat) string {
	if c.queue.Abort(rc.SessionKey) {
		return "Aborted."
	}
	return "No active message to abort."
}

// queuedChat enqueues a chat request for the session and returns a ChatStream
// whose Events channel is a wrapped forwarding channel. The caller must
// fully drain (or abandon) Events before the queue will dispatch the next
// request for the same session.
func (c *Coordinator) queuedChat(ctx context.Context, rc *ResolvedChat, content []ai.ContentBlock) (*pkgchannel.ChatStream, error) {
	stream, doneC, err := c.queue.Enqueue(ctx, rc.SessionKey, func(qctx context.Context) (*pkgchannel.ChatStream, error) {
		return c.chatWithRC(qctx, rc, content)
	})
	if err != nil {
		return nil, err
	}

	// Wrap the stream's Events in a forwarding channel that closes doneC once
	// all events have been forwarded. This releases the queue slot.
	out := make(chan pkgchannel.Event, 100)
	go func() {
		defer close(doneC)
		defer close(out)
		for evt := range stream.Events {
			out <- evt
		}
	}()

	return &pkgchannel.ChatStream{
		Events:    out,
		SessionID: stream.SessionID,
	}, nil
}

// chatWithRC streams a chat response using a pre-resolved chat.
func (c *Coordinator) chatWithRC(ctx context.Context, rc *ResolvedChat, content []ai.ContentBlock) (*pkgchannel.ChatStream, error) {
	events, sessionID, err := rc.Chat(ctx, content)
	if err != nil {
		return nil, err
	}

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

// ListAgents returns enabled agents the user can access and the current agent ID.
func (c *Coordinator) ListAgents(ctx context.Context, msg pkgchannel.IncomingMessage) ([]pkgchannel.AgentInfo, string, error) {
	rc, err := c.resolve(ctx, msg)
	if err != nil {
		return nil, "", err
	}

	ac := NewAgentCommander(c.store, c.authStore)
	agents, err := ac.ListForChat(ctx, rc.ChatCtx)
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
var _ pkgchannel.Handler = (*Coordinator)(nil)
