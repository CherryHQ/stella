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
	"go.opentelemetry.io/otel/attribute"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/auth"
	agentaccess "github.com/CherryHQ/stella/internal/core/access"
	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/platform/config"
	"github.com/CherryHQ/stella/internal/platform/home"
	"github.com/CherryHQ/stella/internal/platform/observability"
	"github.com/CherryHQ/stella/internal/sessionmedia"
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

// ListenerCap is the published system/system-agent ceiling for a channel
// instance. It is applied to every resolved actor at event admission.
type ListenerCap = func(context.Context, string, string) (bool, error)

type Coordinator struct {
	serviceManager  agent.ServiceManager
	invalidator     userInvalidator
	store           config.Store
	auth            channelAuthStore
	agentAccess     *agentaccess.Service
	linkCodes       *auth.LinkCodeStore
	vaultRecipient  *age.X25519Recipient
	vaultSvc        *vault.Service
	listFn          func() []pkgchannel.ModelOption
	switchFn        func(provider, model string) error
	queue           *sessionQueue
	groupResolver   GroupResolver
	eventLog        *eventlog.Store
	botRegistry     *BotIdentityRegistry
	groupDispatcher *GroupDispatcher
	db              *pgxpool.Pool
	rootOpener      home.RootOpener
	guests          GuestStore
	guestPolicy     pkgchannel.GuestPolicyResolver
	listenerCap     ListenerCap
	guestLimiter    *guestRateLimiter
	sessionImages   GroupImagePipeline
	// sessionAccess is the Session PEP for the durable router: binding
	// resolution and rotation without a local agent.Service.
	sessionAccess agent.SessionAccessService
	// channelResolver reaches the running adapter instance for outbox
	// dispatch; nil on replicas with no local channels.
	channelResolver ChannelResolver
	// turnAppender commits a run's deferred transcript inside the execution
	// finish transaction (plan D4); nil only in tests without a memory
	// provider, where the deferred store is simply not consulted.
	turnAppender func(ctx context.Context, tx pgx.Tx, session memory.Session, msgs []ai.Message) error
	// ownerTokens, when bound, returns the channel lease token this replica
	// holds; outbox dispatch only proceeds for owned channels.
	ownerTokens OwnerTokenSource
}

// GroupImagePipeline canonicalizes group images. It is the same pipeline
// ordinary sessions use, split across the group's two moments: ingestion only
// persists (most group images never wake an agent), and the turn that does
// wake renders the baseline once.
type GroupImagePipeline interface {
	Persist(context.Context, sessionmedia.Owner, []ai.ContentBlock) ([]ai.ContentBlock, error)
	RenderBaselines(context.Context, sessionmedia.Owner, string, []ai.ContentBlock) ([]ai.ContentBlock, error)
}

// WithSessionImages wires group media canonicalization. Without it a group
// message with images still lands, but its images degrade to the unavailable
// projection instead of becoming canonical references.
func WithSessionImages(images GroupImagePipeline) CoordinatorOption {
	return func(c *Coordinator) { c.sessionImages = images }
}

// OpenAttachment returns the immutable file bytes committed with an outbox
// op — the send boundary on any replica reads the same row.
func (c *Coordinator) OpenAttachment(ctx context.Context, outboxID string) ([]byte, error) {
	data, err := sqlc.New(c.db).GetChannelOutboxAttachment(ctx, outboxID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: no attachment body for op %s", pkgchannel.ErrAttachmentMissing, outboxID)
	}
	return data, err
}

// WithGuestStore enables durable unlinked channel principals.
func WithGuestStore(store GuestStore) CoordinatorOption {
	return func(c *Coordinator) { c.guests = store }
}

// WithGuestPolicyDecoder injects the plugin-owned persisted policy decoder.
// A missing decoder is intentionally fail-closed for guest admission.
func WithGuestPolicyDecoder(decoder pkgchannel.GuestPolicyResolver) CoordinatorOption {
	return func(c *Coordinator) { c.guestPolicy = decoder }
}

// WithListenerCap injects the common system/system-agent ceiling used during
// event admission; the managed listener remains available to other instances.
func WithListenerCap(cap ListenerCap) CoordinatorOption {
	return func(c *Coordinator) { c.listenerCap = cap }
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

// WithEventLog enables group event log append (dedup + canonical ordering).
func WithEventLog(el *eventlog.Store) CoordinatorOption {
	return func(c *Coordinator) {
		c.eventLog = el
		c.groupResolver = el
	}
}

// WithBotRegistry enables bot identity resolution for @mention → agent routing.
func WithBotRegistry(reg *BotIdentityRegistry) CoordinatorOption {
	return func(c *Coordinator) {
		c.botRegistry = reg
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
	return c.ensurePlatformGroupMember(ctx, platform, platformGroupID, "", "", channelID)
}

// EnsurePlatformThreadGroupMember provisions the exact sub-thread group that
// incoming messages resolve to, rather than the parent channel's group. When
// legacyPlatformGroupID is non-empty and the (platformGroupID, platformThreadID)
// triple has no group yet, it is lazily adopted from a pre-existing top-level
// group at (platform, legacyPlatformGroupID, "") instead of starting a new,
// empty history — see eventlog.Store.ResolveGroupIDWithAdoption.
func (c *Coordinator) EnsurePlatformThreadGroupMember(ctx context.Context, platform, platformGroupID, platformThreadID, legacyPlatformGroupID, channelID string) error {
	return c.ensurePlatformGroupMember(ctx, platform, platformGroupID, platformThreadID, legacyPlatformGroupID, channelID)
}

func (c *Coordinator) ensurePlatformGroupMember(ctx context.Context, platform, platformGroupID, platformThreadID, legacyPlatformGroupID, channelID string) error {
	if c.eventLog == nil || c.db == nil {
		return errors.New("group member provisioning not configured")
	}
	groupID, err := c.eventLog.ResolveGroupIDWithAdoption(ctx, platform, platformGroupID, platformThreadID, legacyPlatformGroupID)
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
	if err := validateGroupChannel(ch, platform); err != nil {
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
	slog.Info("ensured platform group member", "platform", platform, "platform_group_id", platformGroupID, "platform_thread_id", platformThreadID, "group_id", groupID, "agent_id", ch.AgentID, "channel_id", channelID)
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

// RegisterBotIdentity records a bot's platform identity for mention resolution
// and binds it durably to the channel row so group-reply ops can fence on the
// responding channel's account. Implements pkgchannel.BotRegistrar.
func (c *Coordinator) RegisterBotIdentity(platform, platformBotID, channelID string) {
	if c.botRegistry != nil {
		c.botRegistry.Register(platform, platformBotID, channelID)
	}
	c.registerChannelAccount(platform, platformBotID, channelID)
}

// registerChannelAccount persists the platform account this channel's adapter
// speaks as. The write is fenced by the lease token this replica holds, so
// only the owning replica's adapter can bind the account — a fenced-out
// adapter's registration is a no-op. The key is never cleared on unregister:
// ops snapshotted against the last-known account must keep rejecting sends
// after the channel is re-bound to a different account.
func (c *Coordinator) registerChannelAccount(platform, platformBotID, channelID string) {
	if c.db == nil || c.ownerTokens == nil || platformBotID == "" {
		return
	}
	token := c.ownerTokens(channelID)
	if token == "" {
		return
	}
	if _, err := sqlc.New(c.db).RegisterChannelRuntimeAccount(context.Background(), sqlc.RegisterChannelRuntimeAccountParams{
		ID:           channelID,
		ChannelType:  platform,
		AccountKey:   pgtype.Text{String: platformBotID, Valid: true},
		RuntimeToken: token,
	}); err != nil {
		slog.Warn("persist channel account identity", "channel", channelID, "platform", platform, "error", err)
	}
}

func (c *Coordinator) UnregisterBotIdentity(platform, platformBotID, channelID string) {
	if c.botRegistry == nil {
		return
	}
	c.botRegistry.Unregister(platform, platformBotID, channelID)
}

// RegisterBotName records a bot's platform display name as the cross-app
// fallback identity. Implements pkgchannel.BotNameRegistrar.
func (c *Coordinator) RegisterBotName(platform, displayName, channelID string) {
	if c.botRegistry == nil {
		return
	}
	c.botRegistry.RegisterName(platform, displayName, channelID)
}

func (c *Coordinator) UnregisterBotName(platform, displayName, channelID string) {
	if c.botRegistry == nil {
		return
	}
	c.botRegistry.UnregisterName(platform, displayName, channelID)
}

var errChannelPluginDisabled = errors.New("channel plugin disabled for actor")

// channelPluginAllowed applies the published system/system-agent ceiling after
// durable channel identity and AgentAccess resolution. A denied actor is
// rejected at dispatch while the managed platform listener remains available
// to other channel instances and actors.
func (c *Coordinator) channelPluginAllowed(ctx context.Context, rc *ResolvedChat) (bool, error) {
	if rc == nil || rc.ChatCtx.Platform == "" || rc.ChatCtx.Platform == webGroupPlatform {
		return true, nil
	}
	pluginID := config.PluginID(config.PluginKindChannel, rc.ChatCtx.Platform)
	if c.listenerCap == nil {
		return true, nil
	}
	allowed, err := c.listenerCap(ctx, pluginID, rc.AgentID)
	if err != nil {
		return false, fmt.Errorf("resolve channel listener capability: %w", err)
	}
	return allowed, nil
}

// channelListenerAllowed checks the published platform ceiling against the
// exact durable channel binding used by a group event or queued publish.
func (c *Coordinator) channelListenerAllowed(ctx context.Context, platform, channelID string) (bool, error) {
	if c.listenerCap == nil || platform == "" || platform == webGroupPlatform {
		return true, nil
	}
	if channelID == "" {
		channelID = platform
	}
	channel, err := validatePlatformChannel(ctx, c.store, platform, channelID)
	if err != nil {
		return false, err
	}
	allowed, err := c.listenerCap(ctx, config.PluginID(config.PluginKindChannel, platform), channel.AgentID)
	if err != nil {
		return false, fmt.Errorf("resolve channel listener capability: %w", err)
	}
	return allowed, nil
}

// HandleIncoming resolves the user once, tries command handling, and if the
// command is not handled, streams a chat response. This avoids double
// resolution when a plugin needs to try commands before messaging.
func (c *Coordinator) HandleIncoming(ctx context.Context, msg pkgchannel.IncomingMessage, command, args string) (string, bool, *pkgchannel.ChatStream, error) {
	ctx, _ = startIngress(ctx, "channel.ingress",
		attribute.String("stella.channel.name", observability.ChannelName(msg.Platform)),
		attribute.String("stella.channel.id", msg.ChannelID),
	)
	handoff := false
	defer func() {
		if !handoff {
			finishIngress(ctx)
		}
	}()

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

	return c.receiveDurable(ctx, msg, command, args)
}

// chatWithRC streams a chat response using a pre-resolved chat.
func (c *Coordinator) chatWithRC(ctx context.Context, rc *ResolvedChat, content []ai.ContentBlock) (*pkgchannel.ChatStream, error) {
	allowed, err := c.channelPluginAllowed(ctx, rc)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, errChannelPluginDisabled
	}
	// This closure runs only when the per-session queue dispatches. Re-authorize
	// immediately before Chat so a policy change after Resolve cannot run a turn.
	if err := rc.AuthorizeUse(ctx, c.agentAccess); err != nil {
		return nil, fmt.Errorf("agent execution denied: %w", err)
	}
	events, sessionID, err := rc.Chat(ctx, content)
	if err != nil {
		return nil, err
	}

	return &pkgchannel.ChatStream{
		Events:    forwardAgentEvents(ctx, events),
		SessionID: sessionID,
	}, nil
}

// forwardAgentEvents copies the agent's event stream onto a channel stream.
// A cancelled turn is not a completed one: the cause goes onto the stream for
// a consumer still reading (mirroring chatWeb and chatDispatch), non-blocking
// because the usual canceller is a consumer that already walked away. The
// upstream is then drained so the model never blocks on a dead channel.
func forwardAgentEvents(ctx context.Context, events <-chan agent.Event) chan pkgchannel.Event {
	out := make(chan pkgchannel.Event, 100)
	go func() {
		defer close(out)
		for evt := range events {
			select {
			case out <- convertEvent(evt):
			case <-ctx.Done():
				select {
				case out <- pkgchannel.Event{Err: ctx.Err()}:
				default:
				}
				for range events {
				}
				return
			}
		}
	}()
	return out
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

// ResolveUserRoot resolves the per-user writable root for the sender in msg.
// It performs the same user+agent resolution as HandleIncoming but stops before
// starting a session, so it is cheap and safe to call before file downloads.
// For group sessions, returns the group workspace instead of a per-user one.
func (c *Coordinator) attachmentWorkspace(ctx context.Context, msg pkgchannel.IncomingMessage) (home.WorkspaceRequest, error) {
	channelID := msg.ChannelID
	if channelID == "" {
		channelID = msg.Platform
	}
	rc, err := resolveAttachmentPrincipal(ctx, c.store, c.auth, c.agentAccess, c.groupResolver, c.guests, msg.Platform, channelID, msg.SenderID, msg.SenderIDs, msg.SenderName, msg.ChatID, msg.ThreadID, msg.IsGroup, c.guestPolicy)
	if err != nil {
		return home.WorkspaceRequest{}, fmt.Errorf("resolve attachment principal: %w", err)
	}
	allowed, err := c.channelPluginAllowed(ctx, rc)
	if err != nil {
		return home.WorkspaceRequest{}, err
	}
	if !allowed {
		return home.WorkspaceRequest{}, errChannelPluginDisabled
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
	return mapPublicAccessError(err)
}

func (c *Coordinator) SaveAsset(ctx context.Context, msg pkgchannel.IncomingMessage, fileName string, data []byte) (_ string, resultErr error) {
	req, err := c.attachmentWorkspace(ctx, msg)
	if err != nil {
		return "", mapPublicAccessError(err)
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
	_ pkgchannel.AssetSaveAdmitter = (*Coordinator)(nil)
	_ pkgchannel.AssetSaver        = (*Coordinator)(nil)
	_ pkgchannel.BotRegistrar      = (*Coordinator)(nil)
)
