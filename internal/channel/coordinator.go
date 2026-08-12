package channel

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"strings"
	"time"

	"filippo.io/age"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/agent"
	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/internal/home"
	"github.com/CherryHQ/stella/internal/vault"
	"github.com/CherryHQ/stella/pkg/ai"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// channelAuthStore is the subset of auth store interfaces needed by the channel coordinator.
type channelAuthStore interface {
	auth.UserStore
	auth.LoginIdentityStore
	auth.ChannelIdentityStore
	ListUserAgentIDs(ctx context.Context, userID string) ([]string, error)
}

// feishuEnroller is the auth-owned admission boundary for verified Feishu
// members. The coordinator translates the plugin-neutral request only.
type feishuEnroller interface {
	Enroll(ctx context.Context, input auth.FeishuEnrollmentInput) (auth.FeishuEnrollmentResult, error)
}

// Coordinator implements pkgchannel.Handler. It owns all business logic
// that channels previously called directly: user/agent resolution, session
// management, command handling, and account linking.
// A per-session message queue ensures that only one chat turn runs at a time
// per resolved Stella session; later messages are serialised in arrival order.
// userInvalidator is satisfied by *agent.PoolManager so the coordinator can
// invalidate per-user runners after a /config update without importing PoolManager.
type userInvalidator interface {
	InvalidateUser(userID string) error
}

type Coordinator struct {
	serviceManager       agent.ServiceManager
	invalidator          userInvalidator
	store                config.Store
	auth                 channelAuthStore
	feishuEnroller       feishuEnroller
	agentAccess          *agentaccess.Service
	linkCodes            *auth.LinkCodeStore
	vaultRecipient       *age.X25519Recipient
	vaultSvc             *vault.Service
	listFn               func() []pkgchannel.ModelOption
	switchFn             func(provider, model string) error
	queue                *sessionQueue
	intentClassifier     IntentClassifier
	groupResolver        GroupResolver
	eventLog             *eventlog.Store
	memberLister         GroupMemberLister
	botRegistry          *BotIdentityRegistry
	arbiter              *Arbiter
	semanticGroupArbiter SemanticGroupArbiter
	publisherRegistry    *PublisherRegistry
	groupDispatcher      *GroupDispatcher
	db                   *pgxpool.Pool
	rootOpener           home.RootOpener
	guests               GuestStore
	guestLimiter         *guestRateLimiter
}

// WithGuestStore enables durable unlinked channel principals.
func WithGuestStore(store GuestStore) CoordinatorOption {
	return func(c *Coordinator) { c.guests = store }
}

// CoordinatorOption configures the Coordinator.
type CoordinatorOption func(*Coordinator)

func WithRootOpener(opener home.RootOpener) CoordinatorOption {
	return func(c *Coordinator) { c.rootOpener = opener }
}

// WithCoordinatorAuth configures the coordinator with auth support.
func WithCoordinatorAuth(store channelAuthStore, agentAccess *agentaccess.Service, linkCodes *auth.LinkCodeStore) CoordinatorOption {
	return func(c *Coordinator) {
		c.auth = store
		c.agentAccess = agentAccess
		c.linkCodes = linkCodes
	}
}

// WithFeishuEnrollment configures verified-member enrollment. It is separate
// from identity lookup because only the composition root may supply the
// transactional auth service.
func WithFeishuEnrollment(enroller feishuEnroller) CoordinatorOption {
	return func(c *Coordinator) {
		c.feishuEnroller = enroller
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
		guestLimiter:   newGuestRateLimiter(),
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

// WithEventLog enables group event log append (dedup + canonical ordering).
func WithEventLog(el *eventlog.Store) CoordinatorOption {
	return func(c *Coordinator) {
		c.eventLog = el
		c.groupResolver = el
	}
}

// WithGroupMemberLister enables group membership queries for mention resolution.
func WithGroupMemberLister(lister GroupMemberLister) CoordinatorOption {
	return func(c *Coordinator) {
		c.memberLister = lister
	}
}

// WithBotRegistry enables bot identity resolution for @mention → agent routing.
func WithBotRegistry(reg *BotIdentityRegistry) CoordinatorOption {
	return func(c *Coordinator) {
		c.botRegistry = reg
	}
}

// WithArbiter configures the group arbiter for deciding which agents respond.
func WithArbiter(a *Arbiter) CoordinatorOption {
	return func(c *Coordinator) {
		c.arbiter = a
	}
}

// WithSemanticGroupArbiter configures semantic routing for no-mention group
// messages. When set, no-mention messages are classified instead of using the
// all-members fallback; when unset, no-mention behavior is unchanged.
func WithSemanticGroupArbiter(a SemanticGroupArbiter) CoordinatorOption {
	return func(c *Coordinator) {
		c.semanticGroupArbiter = a
	}
}

// WithPublisherRegistry configures cross-channel group response publishers.
func WithPublisherRegistry(reg *PublisherRegistry) CoordinatorOption {
	return func(c *Coordinator) {
		c.publisherRegistry = reg
	}
}

// WithGroupDispatcher configures the durable group dispatcher wake path.
func WithGroupDispatcher(dispatcher *GroupDispatcher) CoordinatorOption {
	return func(c *Coordinator) {
		c.groupDispatcher = dispatcher
	}
}

func (c *Coordinator) SetGroupDispatcher(dispatcher *GroupDispatcher) {
	c.groupDispatcher = dispatcher
}

// WithDB gives the coordinator direct DB access for group member management.
func WithDB(db *pgxpool.Pool) CoordinatorOption {
	return func(c *Coordinator) {
		c.db = db
	}
}

// EnsurePlatformGroupMember resolves the internal group ID for a platform group
// and registers the channel's agent as a member. Safe to call repeatedly.
func (c *Coordinator) EnsurePlatformGroupMember(ctx context.Context, platform, platformGroupID, channelID string) error {
	if c.eventLog == nil || c.db == nil {
		return errors.New("group member provisioning not configured")
	}
	groupID, err := c.eventLog.ResolveGroupID(ctx, platform, platformGroupID, "")
	if err != nil {
		return fmt.Errorf("resolve group: %w", err)
	}
	tx, err := c.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin group member update: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := sqlc.New(tx)
	lockedChannel, err := q.GetChannelForUpdate(ctx, channelID)
	if err != nil {
		return fmt.Errorf("lock channel %q: %w", channelID, err)
	}
	if !lockedChannel.AgentID.Valid {
		return fmt.Errorf("channel %q has no bound agent", channelID)
	}
	ch := config.Channel{
		ID:      lockedChannel.ID,
		Type:    lockedChannel.Type,
		AgentID: lockedChannel.AgentID.String,
		Enabled: lockedChannel.Enabled,
	}
	if err := validateGroupChannel(ch, platform, ch.AgentID); err != nil {
		return fmt.Errorf("channel %q cannot join platform group: %w", channelID, err)
	}
	boundAgent, err := q.GetAgent(ctx, ch.AgentID)
	if err != nil {
		return fmt.Errorf("get channel agent %q: %w", ch.AgentID, err)
	}
	if !boundAgent.Enabled {
		return fmt.Errorf("channel agent %q is disabled", ch.AgentID)
	}
	if _, err := q.GetGroupStateByIDForUpdate(ctx, groupID); err != nil {
		return fmt.Errorf("lock group: %w", err)
	}
	members, err := q.ListGroupMembers(ctx, groupID)
	if err != nil {
		return fmt.Errorf("list group members: %w", err)
	}
	for _, member := range members {
		if member.ReplyChannelID == channelID && member.AgentID != ch.AgentID {
			if err := q.RemoveGroupMember(ctx, sqlc.RemoveGroupMemberParams{GroupID: groupID, AgentID: member.AgentID}); err != nil {
				return fmt.Errorf("remove stale group member: %w", err)
			}
		}
	}
	if _, err := q.AddGroupMember(ctx, sqlc.AddGroupMemberParams{
		GroupID:        groupID,
		AgentID:        ch.AgentID,
		ReplyChannelID: channelID,
	}); err != nil {
		return fmt.Errorf("add group member: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit group member update: %w", err)
	}
	slog.Info("ensured platform group member", "platform", platform, "platform_group_id", platformGroupID, "group_id", groupID, "agent_id", ch.AgentID, "channel_id", channelID)
	return nil
}

// RemovePlatformGroupMember removes the channel's agent from a platform group.
func (c *Coordinator) RemovePlatformGroupMember(ctx context.Context, platform, platformGroupID, channelID string) error {
	if c.eventLog == nil || c.db == nil {
		return errors.New("group member provisioning not configured")
	}
	groupID, err := c.eventLog.ResolveGroupID(ctx, platform, platformGroupID, "")
	if err != nil {
		return fmt.Errorf("resolve group: %w", err)
	}
	ch, err := c.store.GetChannel(ctx, channelID)
	if err != nil {
		return fmt.Errorf("get channel %q: %w", channelID, err)
	}
	if ch.AgentID == "" {
		return nil
	}
	q := sqlc.New(c.db)
	if err := q.RemoveGroupMember(ctx, sqlc.RemoveGroupMemberParams{
		GroupID: groupID,
		AgentID: ch.AgentID,
	}); err != nil {
		return fmt.Errorf("remove group member: %w", err)
	}
	slog.Info("removed platform group member", "platform", platform, "platform_group_id", platformGroupID, "group_id", groupID, "agent_id", ch.AgentID)
	return nil
}

// RegisterBotIdentity records a bot's platform identity for mention resolution.
// Implements pkgchannel.BotRegistrar.
func (c *Coordinator) RegisterBotIdentity(platform, platformBotID, channelID string) {
	if c.botRegistry == nil {
		return
	}
	c.botRegistry.Register(platform, platformBotID, channelID)
}

func (c *Coordinator) UnregisterBotIdentity(platform, platformBotID, channelID string) {
	if c.botRegistry == nil {
		return
	}
	c.botRegistry.Unregister(platform, platformBotID, channelID)
}

func (c *Coordinator) RegisterGroupPublisher(channelID string, publisher GroupPublisher) {
	if c.publisherRegistry == nil {
		return
	}
	c.publisherRegistry.Register(channelID, publisher)
}

func (c *Coordinator) UnregisterGroupPublisher(channelID string) {
	if c.publisherRegistry == nil {
		return
	}
	c.publisherRegistry.Unregister(channelID)
}

// resolve performs the full user -> agent -> pool -> session key resolution.
func (c *Coordinator) resolve(ctx context.Context, msg pkgchannel.IncomingMessage) (*ResolvedChat, error) {
	channelID := msg.ChannelID
	if channelID == "" {
		channelID = msg.Platform
	}

	return ResolveWithChannel(ctx, c.serviceManager, c.store, c.auth, c.agentAccess, c.groupResolver, c.guests, msg.Platform, channelID, msg.SenderID, msg.SenderIDs, msg.SenderName, msg.ChatID, msg.ThreadID, msg.IsGroup)
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

	if msg.IsGroup && c.eventLog != nil {
		return c.handleGroupIncoming(ctx, msg, command, args)
	}

	rc, err := c.resolve(ctx, msg)
	if err != nil {
		return "", false, nil, err
	}
	if rc.GuestID != "" && !c.guestLimiter.allow(rc.GuestID, rc.GuestMessageLimitPerMinute) {
		return "Guest message rate limit exceeded. Try again in a minute.", true, nil, nil
	}

	return c.handleResolvedIncoming(ctx, rc, msg, command, args)
}

func (c *Coordinator) handleResolvedIncoming(ctx context.Context, rc *ResolvedChat, msg pkgchannel.IncomingMessage, command, args string) (string, bool, *pkgchannel.ChatStream, error) {
	if rc.GuestID != "" {
		if !textOnly(msg.Content) {
			return "Guest chat currently supports text messages only.", true, nil, nil
		}
		switch strings.ToLower(command) {
		case "", "/new", "/abort", "/help", "/compact":
		default:
			return "This command is not available in guest chat.", true, nil, nil
		}
	}
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
		// /new runs through the session queue, so it cannot go through the
		// stateless shared command handler.
		if command == "/new" {
			return c.handleNewSessionCommand(ctx, rc, msg), true, nil, nil
		}
		if command == "/compact" {
			if err := rc.AuthorizeUse(ctx, c.agentAccess); err != nil {
				return fmt.Sprintf("Compaction failed: %v", err), true, nil, nil
			}
		}
		if resp, ok := HandleCommand(ctx, rc, command+" "+args, msg.SenderID); ok {
			return resp, true, nil, nil
		}
	}

	if rc.GuestID == "" && c.intentClassifier != nil {
		intent := c.intentClassifier.Classify(ctx, rc.AgentID, msg.Content)
		switch intent {
		case IntentAbort:
			return c.handleAbort(rc), true, nil, nil
		case IntentNew:
			// Deliberately not executed here. Typing `/new` is consent; guessing
			// "新会话" from a short phrase is not, and a wrong guess throws away the
			// user's context. The message falls through to a normal turn, where the
			// agent answers in words and points the user at the explicit command.
		case IntentCompact:
			if err := rc.AuthorizeUse(ctx, c.agentAccess); err != nil {
				return fmt.Sprintf("Compaction failed: %v", err), true, nil, nil
			}
			if resp, ok := HandleCommand(ctx, rc, IntentToCommand(intent), msg.SenderID); ok {
				return resp, true, nil, nil
			}
		case IntentHelp:
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

func textOnly(content []ai.ContentBlock) bool {
	for _, block := range content {
		if _, ok := block.(ai.TextContent); !ok {
			return false
		}
	}
	return true
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

// handleNewSessionCommand starts a fresh session for this chat. The rotation is
// queued behind any in-flight turn on the same session: aborting the user's
// running work on a reset request would be surprising, and rotating underneath it
// would land its reply in a session the user already left.
func (c *Coordinator) handleNewSessionCommand(ctx context.Context, rc *ResolvedChat, msg pkgchannel.IncomingMessage) string {
	receipt := chatReceiptForMessage(c.receiptQueries(), rc, msg, newSessionCommand)
	return rotateChatSession(ctx, rc, receipt, c.queue, func(authCtx context.Context) error {
		return rc.AuthorizeUse(authCtx, c.agentAccess)
	})
}

// receiptQueries returns the store backing command receipts, or nil when the
// coordinator runs without a database (tests); a nil store makes every receipt
// inert, which degrades to the unguarded pre-receipt behavior.
func (c *Coordinator) receiptQueries() *sqlc.Queries {
	if c.db == nil {
		return nil
	}
	return sqlc.New(c.db)
}

// handleAbort cancels the currently-running request for the resolved session.
func (c *Coordinator) handleAbort(rc *ResolvedChat) string {
	if c.queue.Abort(rc.queueKey()) {
		return "Aborted."
	}
	return "No active message to abort."
}

// queuedChat enqueues a chat request for the session and returns a ChatStream
// whose Events channel is a wrapped forwarding channel. The caller must
// fully drain (or abandon) Events before the queue will dispatch the next
// request for the same session.
func (c *Coordinator) queuedChat(ctx context.Context, rc *ResolvedChat, content []ai.ContentBlock) (*pkgchannel.ChatStream, error) {
	stream, doneC, err := c.queue.Enqueue(ctx, rc.queueKey(), func(qctx context.Context) (*pkgchannel.ChatStream, error) {
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
	// This closure runs only when the per-session queue dispatches. Re-authorize
	// immediately before Chat so a policy change after Resolve cannot run a turn.
	if err := rc.AuthorizeUse(ctx, c.agentAccess); err != nil {
		return nil, fmt.Errorf("agent execution denied: %w", err)
	}
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

func convertEvent(evt agent.Event) pkgchannel.Event {
	out := pkgchannel.Event{
		Text:       evt.Text,
		Reasoning:  evt.Reasoning,
		References: evt.References,
		Err:        evt.Err,
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
			ID:         evt.ToolUse.ID,
			Tool:       evt.ToolUse.Tool,
			Status:     evt.ToolUse.Status,
			Input:      evt.ToolUse.Input,
			Arguments:  evt.ToolUse.Arguments,
			Detail:     evt.ToolUse.Detail,
			Content:    evt.ToolUse.Content,
			References: evt.ToolUse.References,
		}
		// Fan the tool's references out to the event-level field so channel
		// consumers that read Event.References (e.g. Feishu) still receive them
		// without the runner having to set the same slice twice.
		if len(evt.ToolUse.References) > 0 {
			out.References = evt.ToolUse.References
		}
	}
	return out
}

// ProvisionUser delegates verified Feishu-member enrollment to auth. It never
// performs account or identity policy in the channel domain.
func (c *Coordinator) ProvisionUser(ctx context.Context, req pkgchannel.ProvisionRequest) error {
	if c.feishuEnroller == nil {
		return errors.New("provision: Feishu enrollment not configured")
	}
	if req.Platform != pkgchannel.PlatformFeishu {
		return errors.New("provision: unsupported platform")
	}
	_, err := c.feishuEnroller.Enroll(ctx, auth.FeishuEnrollmentInput{
		UnionID:   req.ExternalID,
		TenantKey: req.TenantKey,
		Email:     req.Email,
		Name:      req.Name,
		AvatarURL: req.AvatarURL,
	})
	if err != nil {
		return fmt.Errorf("provision: enroll verified Feishu member: %w", err)
	}
	return nil
}

// ResolveUserRoot resolves the per-user writable root for the sender in msg.
// It performs the same user+agent resolution as HandleIncoming but stops before
// starting a session, so it is cheap and safe to call before file downloads.
// For group sessions, returns the group workspace instead of a per-user one.
func (c *Coordinator) attachmentWorkspace(ctx context.Context, msg pkgchannel.IncomingMessage) (home.WorkspaceRequest, error) {
	channelID := msg.ChannelID
	if channelID == "" {
		channelID = msg.Platform
	}
	rc, err := resolveAttachmentPrincipal(ctx, c.store, c.auth, c.agentAccess, c.groupResolver, c.guests, msg.Platform, channelID, msg.SenderID, msg.SenderIDs, msg.SenderName, msg.ChatID, msg.ThreadID, msg.IsGroup)
	if err != nil {
		return home.WorkspaceRequest{}, fmt.Errorf("resolve attachment principal: %w", err)
	}
	if rc.GuestID != "" {
		return home.WorkspaceRequest{}, agentaccess.ErrForbidden
	}
	if err := rc.AuthorizeUse(ctx, c.agentAccess); err != nil {
		return home.WorkspaceRequest{}, err
	}
	return home.WorkspaceRequest{UserID: rc.User.ID, GroupID: rc.GroupID, AgentID: rc.AgentID}, nil
}

func (c *Coordinator) AdmitAssetSave(ctx context.Context, msg pkgchannel.IncomingMessage) error {
	_, err := c.attachmentWorkspace(ctx, msg)
	return err
}

func (c *Coordinator) SaveAsset(ctx context.Context, msg pkgchannel.IncomingMessage, fileName string, data []byte) (_ string, resultErr error) {
	req, err := c.attachmentWorkspace(ctx, msg)
	if err != nil {
		return "", err
	}
	if len(data) > pkgchannel.MaxInboundAttachmentBytes {
		return "", fmt.Errorf("attachment exceeds %d bytes", pkgchannel.MaxInboundAttachmentBytes)
	}
	if c.rootOpener == nil {
		return "", errors.New("home root opener not configured")
	}
	root, err := c.rootOpener.OpenRoot(ctx, req, home.RootPrincipalData, home.RootReadWrite)
	if err != nil {
		return "", fmt.Errorf("open attachment root: %w", err)
	}
	defer func() {
		closeErr := root.Close()
		if closeErr != nil && resultErr == nil {
			resultErr = fmt.Errorf("%w: close attachment root: %w", home.ErrOutcomeUnknown, closeErr)
		} else {
			resultErr = errors.Join(resultErr, closeErr)
		}
	}()
	name := path.Base(strings.ReplaceAll(fileName, "\\", "/"))
	if name == "." || name == "" {
		name = "attachment"
	}
	id, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("generate attachment id: %w", err)
	}
	assetName := fmt.Sprintf("%d_%s-%s", time.Now().UnixNano(), id.String(), name)
	if err := root.Mkdir(ctx, "assets", 0o700, home.MkdirOptions{Parents: true}); err != nil {
		return "", err
	}
	if err := root.Upload(ctx, path.Join("assets", assetName), bytes.NewReader(data), home.WriteOptions{Mode: 0o600, MaxBytes: pkgchannel.MaxInboundAttachmentBytes, Sync: true}); err != nil {
		return "", err
	}
	return "$STELLA_ASSETS_DIR/" + assetName, nil
}

// compile-time checks.
var (
	_ pkgchannel.Handler           = (*Coordinator)(nil)
	_ pkgchannel.Provisioner       = (*Coordinator)(nil)
	_ pkgchannel.AssetSaveAdmitter = (*Coordinator)(nil)
	_ pkgchannel.AssetSaver        = (*Coordinator)(nil)
	_ pkgchannel.BotRegistrar      = (*Coordinator)(nil)
)
