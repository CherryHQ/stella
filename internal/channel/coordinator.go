package channel

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"strings"
	"time"

	"filippo.io/age"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/agent"
	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	agentruntime "github.com/CherryHQ/stella/internal/agent/runtime"
	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/agentrun"
	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/internal/home"
	"github.com/CherryHQ/stella/internal/memory"
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

type sessionImageEnricher interface {
	Enrich(ctx context.Context, userID, agentID string, blocks []ai.ContentBlock) ([]ai.ContentBlock, error)
}

type transactionalSessionImageEnricher interface {
	EnrichWithQueries(ctx context.Context, q *sqlc.Queries, userID, agentID string, blocks []ai.ContentBlock) ([]ai.ContentBlock, error)
}

type Coordinator struct {
	serviceManager    agent.ServiceManager
	invalidator       userInvalidator
	store             config.Store
	auth              channelAuthStore
	feishuEnroller    feishuEnroller
	agentAccess       *agentaccess.Service
	linkCodes         *auth.LinkCodeStore
	vaultRecipient    *age.X25519Recipient
	vaultSvc          *vault.Service
	listFn            func() []pkgchannel.ModelOption
	switchFn          func(provider, model string) error
	queue             *sessionQueue
	intentClassifier  IntentClassifier
	groupResolver     GroupResolver
	eventLog          *eventlog.Store
	botRegistry       *BotIdentityRegistry
	publisherRegistry *PublisherRegistry
	// publisherReconstructor builds an egress client on demand from durable
	// channel state, so a replica that never registered a listener can still
	// deliver an accepted reply.
	publisherReconstructor DurablePublisherReconstructor
	groupDispatcher        *GroupDispatcher
	db                     *pgxpool.Pool
	rootOpener             home.RootOpener
	guests                 GuestStore
	guestLimiter           *guestRateLimiter
	sessionImages          sessionImageEnricher
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

// WithSessionImagePipeline makes raw channel attachments durable before acknowledgement.
func WithSessionImagePipeline(images sessionImageEnricher) CoordinatorOption {
	return func(c *Coordinator) { c.sessionImages = images }
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

// WithBotRegistry enables bot identity resolution for @mention → agent routing.
func WithBotRegistry(reg *BotIdentityRegistry) CoordinatorOption {
	return func(c *Coordinator) {
		c.botRegistry = reg
	}
}

// WithPublisherRegistry configures cross-channel group response publishers.
func WithPublisherRegistry(reg *PublisherRegistry) CoordinatorOption {
	return func(c *Coordinator) {
		c.publisherRegistry = reg
	}
}

// WithDurablePublisherReconstructor configures on-demand channel egress
// reconstruction for durable/non-leader dispatch execution.
func WithDurablePublisherReconstructor(reconstructor DurablePublisherReconstructor) CoordinatorOption {
	return func(c *Coordinator) { c.publisherReconstructor = reconstructor }
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

// AdmitAttachments synchronously crosses the durable acceptance boundary for
// an attachment-bearing platform delivery. Adapters call it after downloading
// expiring URLs and before publishing any visible acknowledgement. The normal
// HandleIncoming call later observes the same stable receipt and executes it.
func (c *Coordinator) AdmitAttachments(ctx context.Context, msg pkgchannel.IncomingMessage) error {
	if !ai.HasAttachment(msg.Content) || c.db == nil {
		return nil
	}
	if msg.IsGroup && c.eventLog != nil {
		result, err := c.appendGroupMessage(ctx, msg)
		if err != nil {
			return fmt.Errorf("group attachment admission: %w", err)
		}
		if result.Inserted && c.groupDispatcher != nil {
			c.groupDispatcher.Wake()
		}
		return nil
	}
	rc, err := c.resolve(ctx, msg)
	if err != nil {
		return err
	}
	if rc.GuestID != "" {
		return errors.New("guest chat currently supports text messages only")
	}
	_, _, err = c.admitDurableQueuedChat(ctx, rc, msg)
	return err
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
	stream, duplicate, err := c.durableQueuedChat(ctx, rc, msg)
	if err != nil {
		return "", false, nil, err
	}
	if duplicate {
		return "Message already accepted.", true, nil, nil
	}
	return "", false, stream, nil
}

// durableQueuedChat makes PostgreSQL's per-ChatBinding sequence the admission
// authority whenever a platform provides a stable delivery ID. The local queue
// remains a fairness optimization for the winning replica only.
func (c *Coordinator) durableQueuedChat(ctx context.Context, rc *ResolvedChat, msg pkgchannel.IncomingMessage) (*pkgchannel.ChatStream, bool, error) {
	if c.db == nil {
		stream, err := c.queuedChat(ctx, rc, msg.Content)
		return stream, false, err
	}
	row, duplicate, err := c.admitDurableQueuedChat(ctx, rc, msg)
	if err != nil || duplicate {
		return nil, duplicate, err
	}
	q := sqlc.New(c.db)
	for {
		switch row.Status {
		case "completed", "rejected":
			return nil, true, nil
		case "blocked":
			if _, err := q.RetryBlockedChannelBindingFIFO(ctx, row.ID); err != nil {
				return nil, false, err
			}
		case "pending":
			claimed, err := q.ClaimChannelBindingFIFOHead(ctx, row.ID)
			if err == nil {
				row = claimed
				goto admitted
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return nil, false, fmt.Errorf("claim channel FIFO input: %w", err)
			}
		case "running":
			if row.RunID.Valid {
				run, runErr := q.GetAgentRun(ctx, row.RunID.String)
				if runErr == nil && run.Status != "running" {
					_, _ = q.CompleteChannelBindingFIFO(ctx, sqlc.CompleteChannelBindingFIFOParams{RunID: row.RunID, ID: row.ID})
					return nil, true, nil
				}
			}
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, false, ctx.Err()
		case <-timer.C:
		}
		row, err = q.GetChannelBindingFIFO(ctx, row.ID)
		if err != nil {
			return nil, false, err
		}
	}

admitted:
	content, err := ai.UnmarshalContentBlocks(row.Payload)
	if err != nil {
		_, _ = q.BlockChannelBindingFIFO(context.WithoutCancel(ctx), sqlc.BlockChannelBindingFIFOParams{
			Reason: "invalid_durable_payload", BackoffSeconds: 300, ID: row.ID,
		})
		return nil, false, fmt.Errorf("decode channel FIFO input: %w", err)
	}
	content = ai.ProjectFileRefs(content)
	completion := agentruntime.NewCompletionBarrier()
	stream, err := c.queuedChatWithOptions(ctx, rc, content,
		agentruntime.WithChannelFIFOClaim(row.ID, row.ClaimToken.String),
		agentruntime.WithCompletionBarrier(completion))
	if err != nil {
		completion.Fail(err)
		completion.Release()
		_, _ = q.BlockChannelBindingFIFO(context.WithoutCancel(ctx), sqlc.BlockChannelBindingFIFOParams{
			Reason: "admission_failed", BackoffSeconds: 1, ID: row.ID,
		})
		return nil, false, err
	}
	row, err = q.GetChannelBindingFIFO(ctx, row.ID)
	if err != nil {
		completion.Release()
		_, _ = q.BlockChannelBindingFIFO(context.WithoutCancel(ctx), sqlc.BlockChannelBindingFIFOParams{
			Reason: "run_link_unknown", BackoffSeconds: 1, ID: row.ID,
		})
		return nil, false, fmt.Errorf("link channel FIFO to AgentRun: %w", err)
	}
	if !row.RunID.Valid {
		completion.Release()
		return nil, false, errors.New("link channel FIFO to AgentRun: admission committed without run link")
	}
	operationCtx, err := completion.Context(context.WithoutCancel(ctx))
	if err != nil {
		completion.Release()
		return nil, false, fmt.Errorf("bind channel publish operation fence: %w", err)
	}
	runID := row.RunID
	fifoID := row.ID
	out := make(chan pkgchannel.Event, 100)
	go func() {
		defer close(out)
		defer completion.Release()
		for event := range stream.Events {
			select {
			case out <- event:
			case <-ctx.Done():
			}
		}
		finishCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		finishCtx, err := completion.Context(finishCtx)
		if err != nil {
			slog.Error("bind durable channel FIFO completion fence", "fifo_id", fifoID, "error", err)
			return
		}
		tx, err := c.db.Begin(finishCtx)
		if err == nil {
			defer func() { _ = tx.Rollback(finishCtx) }()
			err = agentrun.ValidateTx(finishCtx, tx)
		}
		if err == nil {
			var rows int64
			rows, err = sqlc.New(tx).CompleteChannelBindingFIFO(finishCtx, sqlc.CompleteChannelBindingFIFOParams{RunID: runID, ID: fifoID})
			if err == nil && rows != 1 {
				err = agentrun.ErrLeaseLost
			}
		}
		if err == nil {
			err = tx.Commit(finishCtx)
		}
		if err != nil {
			slog.Error("complete durable channel FIFO input", "fifo_id", fifoID, "error", err)
		}
	}()
	return &pkgchannel.ChatStream{
		Events: out, SessionID: stream.SessionID,
		OperationCheck: channelOperationCheck(operationCtx),
	}, false, nil
}

func (c *Coordinator) admitDurableQueuedChat(ctx context.Context, rc *ResolvedChat, msg pkgchannel.IncomingMessage) (sqlc.ChannelBindingFifo, bool, error) {
	q := sqlc.New(c.db)
	_, physicalChat := messageDeliveryCoordinates(msg)
	deliveryID, ok := stableChannelDeliveryID(msg)
	if !ok {
		return sqlc.ChannelBindingFifo{}, false, errors.New("channel message has no stable platform delivery identity")
	}
	sourceKey := msg.Platform + ":" + physicalChat + ":" + deliveryID
	row, err := q.GetChannelBindingFIFOBySource(ctx, sqlc.GetChannelBindingFIFOBySourceParams{
		ChannelID: rc.ChatCtx.ChannelID,
		SourceKey: sourceKey,
	})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return sqlc.ChannelBindingFifo{}, false, fmt.Errorf("read channel FIFO receipt: %w", err)
	}
	if err == nil && (row.Status == "completed" || row.Status == "rejected") {
		return row, true, nil
	}
	if err == nil && (row.BindingKey != rc.queueKey() || row.PrincipalID != rc.sessionUserID()) {
		// Stable source identity belongs to the binding/principal accepted with the
		// original delivery. A later account link or channel rebind must not replay
		// that durable input through the newly resolved authority.
		return sqlc.ChannelBindingFifo{}, false, errors.New("channel FIFO receipt belongs to a different accepted binding")
	}
	if errors.Is(err, pgx.ErrNoRows) {
		// Re-authorize before immutable-media persistence: rejected principals
		// must not create blobs or metadata even though no FIFO ack would follow.
		if err := rc.AuthorizeUse(ctx, c.agentAccess); err != nil {
			return sqlc.ChannelBindingFifo{}, false, fmt.Errorf("authorize durable channel input: %w", err)
		}
		tx, err := c.db.Begin(ctx)
		if err != nil {
			return sqlc.ChannelBindingFifo{}, false, fmt.Errorf("begin durable channel input: %w", err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		qtx := sqlc.New(tx)
		canonical, immutableMedia, attachmentBytes, err := c.immutableChannelContentWithQueries(ctx, qtx, rc.User.ID, rc.AgentID, msg.Content)
		if err != nil {
			return sqlc.ChannelBindingFifo{}, false, err
		}
		// Admission happens before the durable acceptance point. In particular,
		// never acknowledge bytes that cannot be replayed without relying on an
		// adapter's temporary buffer or whose principal is no longer authorized.
		if err := ai.ValidateCanonicalContentBlocks(canonical); err != nil {
			return sqlc.ChannelBindingFifo{}, false, fmt.Errorf("validate durable channel input: %w", err)
		}
		payload, err := ai.MarshalContentBlocks(canonical)
		if err != nil {
			return sqlc.ChannelBindingFifo{}, false, fmt.Errorf("encode durable channel input: %w", err)
		}
		row, err = createChannelBindingFIFOWithQueries(ctx, qtx, sqlc.CreateChannelBindingFIFOParams{
			ID: uuid.Must(uuid.NewV7()).String(), ChannelID: rc.ChatCtx.ChannelID,
			BindingKey: rc.queueKey(), PrincipalID: rc.sessionUserID(), SourceKey: sourceKey,
			Kind: "message", Payload: payload, ImmutableMedia: immutableMedia,
			AttachmentBytes: attachmentBytes,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			// A matching receipt wins over quota rejection: stable redelivery is
			// idempotent and must never consume admission budget twice.
			row, err = qtx.GetChannelBindingFIFOBySource(ctx, sqlc.GetChannelBindingFIFOBySourceParams{
				ChannelID: rc.ChatCtx.ChannelID,
				SourceKey: sourceKey,
			})
			if errors.Is(err, pgx.ErrNoRows) {
				return sqlc.ChannelBindingFifo{}, false, errors.New("channel FIFO quota exceeded")
			}
		}
		if err != nil {
			return sqlc.ChannelBindingFifo{}, false, fmt.Errorf("persist channel FIFO input: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return sqlc.ChannelBindingFifo{}, false, fmt.Errorf("commit durable channel input: %w", err)
		}
	}
	return row, false, nil
}

func channelOperationCheck(guardCtx context.Context) func(context.Context) error {
	return func(ctx context.Context) error {
		guarded, ok := agentrun.InheritGuard(ctx, guardCtx)
		if !ok {
			return agentrun.ErrLeaseLost
		}
		return agentrun.Check(guarded)
	}
}

type immutableChannelMedia struct {
	MediaID  string `json:"media_id"`
	SizeByte int64  `json:"size_bytes"`
}

func (c *Coordinator) immutableChannelContentWithQueries(ctx context.Context, q *sqlc.Queries, userID, agentID string, blocks []ai.ContentBlock) ([]ai.ContentBlock, json.RawMessage, int64, error) {
	hasRawAttachment := false
	for _, block := range blocks {
		switch value := block.(type) {
		case ai.ImageContent:
			hasRawAttachment = true
			if _, err := base64.StdEncoding.DecodeString(value.Data); err != nil {
				return nil, nil, 0, fmt.Errorf("decode channel attachment: %w", err)
			}
		case ai.FileContent:
			hasRawAttachment = true
			if value.Path != pkgchannel.ImmutableAssetPath(value.Name, value.Data) {
				return nil, nil, 0, errors.New("channel file attachment is not stored at its immutable content-addressed path")
			}
		}
	}
	canonical := ai.CloneContentBlocks(blocks)
	if hasRawAttachment {
		if c.sessionImages == nil {
			return nil, nil, 0, errors.New("immutable channel attachment storage is unavailable")
		}
		var err error
		if q == nil {
			canonical, err = c.sessionImages.Enrich(ctx, userID, agentID, canonical)
		} else if transactional, ok := c.sessionImages.(transactionalSessionImageEnricher); ok {
			canonical, err = transactional.EnrichWithQueries(ctx, q, userID, agentID, canonical)
		} else {
			return nil, nil, 0, errors.New("transactional immutable attachment storage is unavailable")
		}
		if err != nil {
			return nil, nil, 0, fmt.Errorf("publish immutable channel attachments: %w", err)
		}
	}
	if q == nil {
		q = sqlc.New(c.db)
	}
	immutableMedia, attachmentBytes, err := immutableMediaMetadataForUser(ctx, q, userID, canonical)
	if err != nil {
		return nil, nil, 0, err
	}
	return canonical, immutableMedia, attachmentBytes, nil
}

type immutableMediaQueries interface {
	ListMediaByIDs(context.Context, []string) ([]sqlc.CtxMedium, error)
	ListMediaByIDsForUser(context.Context, sqlc.ListMediaByIDsForUserParams) ([]sqlc.CtxMedium, error)
}

func immutableMediaMetadataForUser(ctx context.Context, q immutableMediaQueries, userID string, blocks []ai.ContentBlock) (json.RawMessage, int64, error) {
	ids := make([]string, 0)
	seen := make(map[string]struct{})
	for _, block := range blocks {
		var id string
		switch ref := block.(type) {
		case ai.ImageRefContent:
			id = ref.MediaID
		case ai.FileRefContent:
			id = ref.MediaID
		}
		if id != "" {
			if _, exists := seen[id]; !exists {
				seen[id] = struct{}{}
				ids = append(ids, id)
			}
		}
	}
	if len(ids) == 0 {
		return json.RawMessage(`[]`), 0, nil
	}
	var rows []sqlc.CtxMedium
	var err error
	if userID == "" {
		// Group event-log payloads were authorized when appended and are
		// reconstructed by globally unique immutable ID.
		rows, err = q.ListMediaByIDs(ctx, ids)
	} else {
		// Direct channel input may contain a caller-supplied ImageRefContent.
		// Do not let it bind another principal's immutable media into this FIFO.
		rows, err = q.ListMediaByIDsForUser(ctx, sqlc.ListMediaByIDsForUserParams{UserID: userID, MediaIds: ids})
	}
	if err != nil {
		return nil, 0, fmt.Errorf("load immutable channel media metadata: %w", err)
	}
	if len(rows) != len(ids) {
		return nil, 0, errors.New("immutable channel media reference is missing")
	}
	mediaByID := make(map[string]int64, len(rows))
	for _, row := range rows {
		mediaByID[row.ID] = row.SizeBytes
	}
	media := make([]immutableChannelMedia, 0, len(ids))
	var attachmentBytes int64
	for _, id := range ids {
		size := mediaByID[id]
		if size <= 0 {
			return nil, 0, errors.New("immutable channel media has invalid size")
		}
		attachmentBytes += size
		media = append(media, immutableChannelMedia{MediaID: id, SizeByte: size})
	}
	encoded, err := json.Marshal(media)
	if err != nil {
		return nil, 0, fmt.Errorf("encode immutable channel media: %w", err)
	}
	return encoded, attachmentBytes, nil
}

func stableChannelDeliveryID(msg pkgchannel.IncomingMessage) (string, bool) {
	// A timestamp/content hash is not a delivery identity: providers can
	// normalize timestamps differently on redelivery, while two legitimate
	// same-content messages can share one timestamp. Asynchronous durable input
	// therefore fails closed unless the adapter supplies its immutable platform
	// message ID.
	return msg.MessageID, msg.MessageID != ""
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
	if c.db != nil {
		return c.handleDurableNewSessionCommand(ctx, rc, msg)
	}
	receipt := chatReceiptForMessage(c.receiptQueries(), rc, msg, newSessionCommand)
	return rotateChatSession(ctx, rc, receipt, c.queue, func(authCtx context.Context) error {
		return rc.AuthorizeUse(authCtx, c.agentAccess)
	})
}

// handleDurableNewSessionCommand places /new in the same PostgreSQL FIFO as
// messages. expected_session_id is frozen at acceptance; after a crash or
// historical redelivery, a stale comparison retires the barrier without ever
// rotating its successor.
func (c *Coordinator) handleDurableNewSessionCommand(ctx context.Context, rc *ResolvedChat, msg pkgchannel.IncomingMessage) string {
	deliveryID, ok := stableChannelDeliveryID(msg)
	if !ok {
		return pkgchannel.NewSessionUnverifiableMessage
	}
	if err := rc.AuthorizeUse(ctx, c.agentAccess); err != nil {
		return fmt.Sprintf("Starting a new session failed: %v", err)
	}
	q := sqlc.New(c.db)
	_, physicalChat := messageDeliveryCoordinates(msg)
	sourceKey := "command:" + physicalChat + ":" + deliveryID
	row, err := q.GetChannelBindingFIFOBySource(ctx, sqlc.GetChannelBindingFIFOBySourceParams{
		ChannelID: rc.ChatCtx.ChannelID,
		SourceKey: sourceKey,
	})
	if err == nil && (row.Status == "completed" || row.Status == "rejected") {
		return pkgchannel.SessionAlreadyResetMessage
	}
	if err == nil && (row.BindingKey != rc.queueKey() || row.PrincipalID != rc.sessionUserID()) {
		return "Starting a new session failed: command receipt belongs to a different accepted binding"
	}
	if errors.Is(err, pgx.ErrNoRows) {
		current, currentErr := rc.CurrentSessionForRotation(ctx)
		if currentErr != nil {
			return fmt.Sprintf("Starting a new session failed: %v", currentErr)
		}
		row, err = createChannelBindingFIFO(ctx, c.db, sqlc.CreateChannelBindingFIFOParams{
			ID: uuid.Must(uuid.NewV7()).String(), ChannelID: rc.ChatCtx.ChannelID,
			BindingKey: rc.queueKey(), PrincipalID: rc.sessionUserID(), SourceKey: sourceKey,
			Kind: "new", Payload: json.RawMessage(`[]`), ImmutableMedia: json.RawMessage(`[]`),
			ExpectedSessionID: pgtype.Text{String: current.ID, Valid: true},
		})
		if errors.Is(err, pgx.ErrNoRows) {
			row, err = q.GetChannelBindingFIFOBySource(ctx, sqlc.GetChannelBindingFIFOBySourceParams{
				ChannelID: rc.ChatCtx.ChannelID,
				SourceKey: sourceKey,
			})
			if errors.Is(err, pgx.ErrNoRows) {
				return "Starting a new session failed: channel FIFO quota exceeded"
			}
		}
	}
	if err != nil {
		return fmt.Sprintf("Starting a new session failed: %v", err)
	}
	for {
		switch row.Status {
		case "completed", "rejected":
			return pkgchannel.SessionAlreadyResetMessage
		case "blocked":
			_, _ = q.RetryBlockedChannelBindingFIFO(ctx, row.ID)
		case "pending":
			claimed, claimErr := q.ClaimChannelBindingFIFOHead(ctx, row.ID)
			if claimErr == nil {
				row = claimed
				goto rotate
			}
			if !errors.Is(claimErr, pgx.ErrNoRows) {
				return fmt.Sprintf("Starting a new session failed: %v", claimErr)
			}
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Sprintf("Starting a new session failed: %v", ctx.Err())
		case <-timer.C:
		}
		row, err = q.GetChannelBindingFIFO(ctx, row.ID)
		if err != nil {
			return fmt.Sprintf("Starting a new session failed: %v", err)
		}
	}

rotate:
	reply := pkgchannel.NewSessionStartedMessage
	rotationCtx := memory.WithRotationFence(ctx, memory.RotationFence{
		FIFOID:            row.ID,
		ChannelID:         row.ChannelID,
		BindingKey:        row.BindingKey,
		BindingRevision:   row.BindingRevision,
		ExpectedSessionID: row.ExpectedSessionID.String,
		ClaimToken:        row.ClaimToken.String,
	})
	_, err = rc.RotateSession(rotationCtx, row.ExpectedSessionID.String)
	if errors.Is(err, session.ErrStaleRotation) {
		reply = pkgchannel.SessionAlreadyResetMessage
		err = nil
	}
	if err != nil {
		_, _ = q.BlockChannelBindingFIFO(context.WithoutCancel(ctx), sqlc.BlockChannelBindingFIFOParams{
			Reason: "new_session_rotation_failed", BackoffSeconds: 5, ID: row.ID,
		})
		return fmt.Sprintf("Starting a new session failed: %v", err)
	}
	completed, err := q.CompleteChannelBindingFIFOControl(ctx, sqlc.CompleteChannelBindingFIFOControlParams{
		ID: row.ID, ClaimToken: row.ClaimToken,
	})
	if err != nil || completed == 0 {
		// Rotation may have committed. expected_session_id makes retry safe; do
		// not release or issue a second reset when completion acknowledgement is
		// ambiguous.
		return pkgchannel.NewSessionOutcomeUnknownMessage
	}
	return reply
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
	return c.queuedChatWithOptions(ctx, rc, content)
}

func (c *Coordinator) queuedChatWithOptions(ctx context.Context, rc *ResolvedChat, content []ai.ContentBlock, opts ...agentruntime.Option) (*pkgchannel.ChatStream, error) {
	stream, doneC, err := c.queue.Enqueue(ctx, rc.queueKey(), func(qctx context.Context) (*pkgchannel.ChatStream, error) {
		return c.chatWithRCOptions(qctx, rc, content, opts...)
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
		Events:         out,
		SessionID:      stream.SessionID,
		OperationCheck: stream.OperationCheck,
	}, nil
}

// chatWithRC streams a chat response using a pre-resolved chat.
func (c *Coordinator) chatWithRC(ctx context.Context, rc *ResolvedChat, content []ai.ContentBlock) (*pkgchannel.ChatStream, error) {
	return c.chatWithRCOptions(ctx, rc, content)
}

func (c *Coordinator) chatWithRCOptions(ctx context.Context, rc *ResolvedChat, content []ai.ContentBlock, opts ...agentruntime.Option) (*pkgchannel.ChatStream, error) {
	// This closure runs only when the per-session queue dispatches. Re-authorize
	// immediately before Chat so a policy change after Resolve cannot run a turn.
	if err := rc.AuthorizeUse(ctx, c.agentAccess); err != nil {
		return nil, fmt.Errorf("agent execution denied: %w", err)
	}
	events, sessionID, err := rc.ChatWithRuntimeOptions(ctx, content, opts...)
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
	logicalPath := pkgchannel.ImmutableAssetPath(fileName, data)
	assetName := strings.TrimPrefix(logicalPath, "$STELLA_ASSETS_DIR/")
	if err := root.Mkdir(ctx, "assets", 0o700, home.MkdirOptions{Parents: true}); err != nil {
		return "", err
	}
	if err := root.Upload(ctx, path.Join("assets", assetName), bytes.NewReader(data), home.WriteOptions{Mode: 0o600, MaxBytes: pkgchannel.MaxInboundAttachmentBytes, Sync: true}); err != nil {
		return "", err
	}
	return logicalPath, nil
}

// compile-time checks.
var (
	_ pkgchannel.Handler            = (*Coordinator)(nil)
	_ pkgchannel.Provisioner        = (*Coordinator)(nil)
	_ pkgchannel.AssetSaveAdmitter  = (*Coordinator)(nil)
	_ pkgchannel.AttachmentAdmitter = (*Coordinator)(nil)
	_ pkgchannel.AssetSaver         = (*Coordinator)(nil)
	_ pkgchannel.BotRegistrar       = (*Coordinator)(nil)
)
