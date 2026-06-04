package channel

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"filippo.io/age"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/vault"
	"github.com/CherryHQ/stella/pkg/ai"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

// channelAuthStore is the subset of auth store interfaces needed by the channel coordinator.
type channelAuthStore interface {
	auth.UserStore
	auth.LoginIdentityStore
	auth.ChannelIdentityStore
	ListUserAgentIDs(ctx context.Context, userID string) ([]string, error)
}

// Coordinator implements pkgchannel.Handler. It owns all business logic
// that channels previously called directly: user/agent resolution, session
// management, command handling, account linking, and model/agent switching.
// A per-session message queue ensures that only one chat turn runs at a time
// per resolved Stella session; later messages are serialised in arrival order.
// userInvalidator is satisfied by *agent.PoolManager so the coordinator can
// invalidate per-user runners after a /config update without importing PoolManager.
type userInvalidator interface {
	InvalidateUser(userID string) error
}

type Coordinator struct {
	serviceManager   agent.ServiceManager
	invalidator      userInvalidator
	store            config.Store
	auth             channelAuthStore
	engine           *auth.PolicyEngine
	linkCodes        *auth.LinkCodeStore
	vaultRecipient   *age.X25519Recipient
	vaultSvc         *vault.Service
	listFn           func() []pkgchannel.ModelOption
	switchFn         func(provider, model string) error
	queue            *sessionQueue
	intentClassifier IntentClassifier
	groupResolver    GroupResolver
}

// CoordinatorOption configures the Coordinator.
type CoordinatorOption func(*Coordinator)

// WithCoordinatorAuth configures the coordinator with auth support.
func WithCoordinatorAuth(store channelAuthStore, engine *auth.PolicyEngine, linkCodes *auth.LinkCodeStore) CoordinatorOption {
	return func(c *Coordinator) {
		c.auth = store
		c.engine = engine
		c.linkCodes = linkCodes
	}
}

// WithVaultRecipient sets the master age recipient so channel-provisioned users
// get age keys at creation time.
func WithVaultRecipient(r *age.X25519Recipient) CoordinatorOption {
	return func(c *Coordinator) {
		c.vaultRecipient = r
	}
}

// NewCoordinator creates a Coordinator that satisfies pkgchannel.Handler.
// pm must implement both agent.ServiceManager (for routing) and userInvalidator
// (for /config secret updates). *agent.PoolManager satisfies both.
func NewCoordinator(
	pm interface {
		agent.ServiceManager
		userInvalidator
	},
	store config.Store,
	listFn func() []pkgchannel.ModelOption,
	switchFn func(provider, model string) error,
	opts ...CoordinatorOption,
) *Coordinator {
	c := &Coordinator{
		serviceManager: pm,
		invalidator:    pm,
		store:          store,
		listFn:         listFn,
		switchFn:       switchFn,
		queue:          newSessionQueue(),
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// WithVaultService configures the coordinator with vault secret management.
func WithVaultService(svc *vault.Service) CoordinatorOption {
	return func(c *Coordinator) {
		c.vaultSvc = svc
	}
}

func WithIntentClassifier(classifier IntentClassifier) CoordinatorOption {
	return func(c *Coordinator) {
		c.intentClassifier = classifier
	}
}

// WithGroupResolver enables group session identity resolution (D0/D9).
func WithGroupResolver(gr GroupResolver) CoordinatorOption {
	return func(c *Coordinator) {
		c.groupResolver = gr
	}
}

// resolve performs the full user -> agent -> pool -> session key resolution.
func (c *Coordinator) resolve(ctx context.Context, msg pkgchannel.IncomingMessage) (*ResolvedChat, error) {
	channelID := msg.ChannelID
	if channelID == "" {
		channelID = msg.Platform
	}

	return ResolveWithChannel(ctx, c.serviceManager, c.store, c.auth, c.engine, c.groupResolver, msg.Platform, channelID, msg.SenderID, msg.SenderIDs, msg.SenderName, msg.ChatID, msg.ThreadID, msg.IsGroup)
}

// HandleIncoming resolves the user once, tries command handling, and if the
// command is not handled, streams a chat response. This avoids double
// resolution when a plugin needs to try commands before messaging.
func (c *Coordinator) HandleIncoming(ctx context.Context, msg pkgchannel.IncomingMessage, command, args string) (string, bool, *pkgchannel.ChatStream, error) {
	// Try link code first (before auth resolution, since it creates identity).
	if c.auth != nil && c.linkCodes != nil {
		fullText := command
		if args != "" {
			fullText = command + " " + args
		}
		if resp, ok := TryLinkCodeWithCandidates(ctx, c.auth, c.linkCodes, fullText, msg.Platform, msg.SenderID, msg.SenderIDs, msg.SenderName); ok {
			return resp, true, nil, nil
		}
	}

	rc, err := c.resolve(ctx, msg)
	if err != nil {
		return "", false, nil, err
	}

	return c.handleResolvedIncoming(ctx, rc, msg, command, args)
}

func (c *Coordinator) handleResolvedIncoming(ctx context.Context, rc *ResolvedChat, msg pkgchannel.IncomingMessage, command, args string) (string, bool, *pkgchannel.ChatStream, error) {
	// Try shared commands.
	if command != "" {
		command = strings.ToLower(command)
		// /abort is handled here directly so it can cancel the active message.
		if command == "/abort" {
			return c.handleAbort(rc), true, nil, nil
		}
		if command == "/config" {
			return c.handleConfigCommand(ctx, rc, args)
		}
		if resp, ok := HandleCommand(ctx, rc, command+" "+args, msg.SenderID); ok {
			return resp, true, nil, nil
		}
	}

	if c.intentClassifier != nil {
		intent := c.intentClassifier.Classify(ctx, rc.AgentID, msg.Content)
		switch intent {
		case IntentAbort:
			return c.handleAbort(rc), true, nil, nil
		case IntentHelp, IntentNew, IntentCompact:
			if resp, ok := HandleCommand(ctx, rc, IntentToCommand(intent), msg.SenderID); ok {
				return resp, true, nil, nil
			}
		}
	}

	// Not a command or recognized intent — enqueue a chat response for this session.
	stream, err := c.queuedChat(ctx, rc, msg.Content)
	if err != nil {
		return "", false, nil, err
	}
	return "", false, stream, nil
}

// handleConfigCommand handles /config KEY VALUE: writes to vault, invalidates
// per-user runners, and resumes the conversation with a sanitized synthetic turn
// so the model can continue the blocked task without seeing the secret value.
// On error, returns a plain text error response.
func (c *Coordinator) handleConfigCommand(ctx context.Context, rc *ResolvedChat, args string) (string, bool, *pkgchannel.ChatStream, error) {
	resp, ok := handleConfig(ctx, c.vaultSvc, rc.User.ID, args)
	if !ok {
		return resp, true, nil, nil
	}

	// Extract key for synthetic message (handleConfig already validated len >= 2).
	key := strings.ToUpper(strings.Fields(args)[0])

	// Invalidate all live runners for this user so fresh env is used next turn.
	if err := c.invalidator.InvalidateUser(rc.User.ID); err != nil {
		_ = err
	}

	// Replace the raw /config turn with a sanitized synthetic continuation.
	synthetic := []ai.ContentBlock{
		ai.TextContent{Text: "Credential " + key + " was stored successfully; continue with the user's prior task."},
	}
	stream, err := c.queuedChat(ctx, rc, synthetic)
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
			select {
			case out <- evt:
			case <-ctx.Done():
				// Caller stopped reading, just drain the stream to not block the model
			}
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
			select {
			case out <- convertEvent(evt):
			case <-ctx.Done():
			}
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

	ac := NewAgentCommander(c.store, c.auth)
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

	ac := NewAgentCommander(c.store, c.auth)
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

func convertEvent(evt agent.Event) pkgchannel.Event {
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
	if evt.File != nil {
		out.File = &pkgchannel.FileEvent{
			Path: evt.File.Path,
			Name: evt.File.Name,
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

// ProvisionUser checks whether a channel identity exists for the sender.
// Returns an error if the identity is not found — the user must first log in
// via OIDC and link their channel account.
func (c *Coordinator) ProvisionUser(ctx context.Context, req pkgchannel.ProvisionRequest) error {
	if c.auth == nil {
		return errors.New("provision: auth not configured")
	}
	_, err := c.auth.GetChannelIdentityByPlatform(ctx, req.Platform, req.ExternalID)
	if err == nil {
		return nil
	}
	if !isNotFound(err) {
		return fmt.Errorf("provision: lookup channel identity: %w", err)
	}
	_, _, ok, linkErr := linkLoginIdentityAsChannelIdentity(ctx, c.auth, req.Platform, req.ExternalID, req.Name)
	if linkErr != nil {
		return fmt.Errorf("provision: link login identity: %w", linkErr)
	}
	if ok {
		return nil
	}
	return fmt.Errorf("provision: channel identity not found (user must link account via OIDC first): %w", err)
}

// ResolveUserRoot resolves the per-user writable root for the sender in msg.
// It performs the same user+agent resolution as HandleIncoming but stops before
// starting a session, so it is cheap and safe to call before file downloads.
// For group sessions, returns the group workspace instead of a per-user one.
func (c *Coordinator) ResolveUserRoot(ctx context.Context, msg pkgchannel.IncomingMessage) (string, error) {
	rc, err := c.resolve(ctx, msg)
	if err != nil {
		return "", fmt.Errorf("resolve user root: %w", err)
	}
	if rc.GroupID != "" {
		dir, err := agent.SetupGroupWorkspace(rc.AgentID, config.StellaHome(), rc.GroupID)
		if err != nil {
			return "", fmt.Errorf("setup group workspace: %w", err)
		}
		return dir, nil
	}
	userDir, err := agent.SetupUserWorkspace(rc.AgentID, config.StellaHome(), rc.User.ID)
	if err != nil {
		return "", fmt.Errorf("setup user workspace: %w", err)
	}
	return userDir, nil
}

// compile-time checks.
var (
	_ pkgchannel.Handler          = (*Coordinator)(nil)
	_ pkgchannel.Provisioner      = (*Coordinator)(nil)
	_ pkgchannel.UserRootResolver = (*Coordinator)(nil)
)
