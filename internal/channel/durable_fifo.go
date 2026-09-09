package channel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/agentrun"
	"github.com/CherryHQ/stella/internal/sessionmedia"
	"github.com/CherryHQ/stella/pkg/ai"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

type durableIngressBypassKey struct{}

func withDurableIngressBypass(ctx context.Context) context.Context {
	return context.WithValue(ctx, durableIngressBypassKey{}, true)
}

func hasDurableIngressBypass(ctx context.Context) bool {
	v, _ := ctx.Value(durableIngressBypassKey{}).(bool)
	return v
}

type durableIngressPayload struct {
	Message            pkgchannel.IncomingMessage `json:"message"`
	Command            string                     `json:"command,omitempty"`
	Args               string                     `json:"args,omitempty"`
	Content            json.RawMessage            `json:"content"`
	ReplyCapabilityRef string                     `json:"reply_capability_ref,omitempty"`
}

// durableReplyCapabilityRef is the only capability material admitted into a
// FIFO envelope. Ciphertext is inserted in the same transaction as the item;
// the platform secret itself never enters payload JSON or an event row.
type durableReplyCapabilityRef struct {
	ID         string
	Kind       string
	Ciphertext string
	ExpiresAt  time.Time
}

type durableCommandReceipt struct {
	channelID string
	chatKey   string
	messageID string
	command   string
	binding   string
}

var errDurableCommandAlreadyHandled = errors.New("durable command was already handled")

// errDurableSourcePrincipal prevents a queued delivery from being retried as
// ordinary transient work after the platform identity that admitted it has
// been rebound. The AgentRun admission callback uses this sentinel while it
// still owns the source transaction, so no model turn can start and the FIFO
// item is left for explicit operator disposition.
var errDurableSourcePrincipal = errors.New("durable channel source principal changed")

const (
	maxDurableReplyCapabilityKindBytes   = 64
	maxDurableReplyCapabilitySecretBytes = 16 << 10
	maxDurableReplyCapabilityCipherBytes = 64 << 10
)

type durableIngressWaiter struct {
	events chan pkgchannel.Event
	stream *pkgchannel.ChatStream
	ctx    context.Context
}

// durableCompletionProxy is the adapter-facing completion fence for a source
// whose durable facts settle after the adapter reports its egress outcome.
// The listener receives the proxy before the consumer has admitted an
// AgentRun, so Check blocks until Bind rather than exposing an unbound runtime
// barrier. Ack only captures the reported outcome and returns; ForwardAck is
// the single terminalization point, called by the source owner after the FIFO
// item and its quota release have settled.
type durableCompletionProxy struct {
	mu        sync.Mutex
	source    pkgchannel.StreamCompletion
	bound     chan struct{}
	ackReady  chan struct{}
	boundOnce sync.Once
	ackOnce   sync.Once
	done      chan struct{}
	doneOnce  sync.Once
	ack       pkgchannel.EgressOutcome
	hasAck    bool
	forwarded bool
	settleErr error
}

func newDurableCompletionProxy() *durableCompletionProxy {
	return &durableCompletionProxy{
		bound: make(chan struct{}), ackReady: make(chan struct{}), done: make(chan struct{}),
	}
}

func (p *durableCompletionProxy) Bind(source pkgchannel.StreamCompletion) {
	if p == nil {
		return
	}
	p.mu.Lock()
	if p.source != nil || p.bound == nil {
		p.mu.Unlock()
		return
	}
	p.source = source
	p.boundOnce.Do(func() { close(p.bound) })
	p.mu.Unlock()
}

func (p *durableCompletionProxy) Check(ctx context.Context) error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	source := p.source
	bound := p.bound
	p.mu.Unlock()
	if source == nil && bound != nil {
		select {
		case <-bound:
		case <-ctx.Done():
			return ctx.Err()
		}
		p.mu.Lock()
		source = p.source
		p.mu.Unlock()
	}
	if source == nil {
		return nil
	}
	return source.Check(ctx)
}

// Ack captures the adapter's reported egress outcome and returns without
// touching the underlying completion. The source owner terminalizes through
// ForwardAck after its durable facts settle. Repeating the same outcome is
// idempotent; a different outcome is a conflict the caller must surface.
func (p *durableCompletionProxy) Ack(_ context.Context, outcome pkgchannel.EgressOutcome) error {
	return p.captureAck(outcome)
}

func (p *durableCompletionProxy) captureAck(outcome pkgchannel.EgressOutcome) error {
	if p == nil {
		return nil
	}
	if !outcome.Valid() {
		return fmt.Errorf("invalid durable egress outcome %q", outcome)
	}
	p.mu.Lock()
	if p.hasAck {
		captured := p.ack
		p.mu.Unlock()
		if captured != outcome {
			return fmt.Errorf("durable egress outcome changed from %q to %q", captured, outcome)
		}
		return nil
	}
	p.ack, p.hasAck = outcome, true
	p.mu.Unlock()
	p.ackOnce.Do(func() { close(p.ackReady) })
	return nil
}

// forceAck is used when the source-side durable boundary fails after a
// publisher has already captured a successful outcome. The outcome was never
// forwarded to the AgentRun, so it is still safe to downgrade it to unknown;
// forwarding delivered while the FIFO item is pending would make recovery
// report a false success.
func (p *durableCompletionProxy) forceAck(outcome pkgchannel.EgressOutcome) error {
	if p == nil {
		return nil
	}
	if !outcome.Valid() {
		return fmt.Errorf("invalid durable egress outcome %q", outcome)
	}
	p.mu.Lock()
	if p.forwarded {
		p.mu.Unlock()
		return errors.New("durable egress outcome already forwarded")
	}
	p.ack, p.hasAck = outcome, true
	p.mu.Unlock()
	p.ackOnce.Do(func() { close(p.ackReady) })
	return nil
}

func (p *durableCompletionProxy) hasSource() bool {
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.source != nil
}

func (p *durableCompletionProxy) Done() <-chan struct{} {
	if p == nil {
		return nil
	}
	return p.done
}

func (p *durableCompletionProxy) AckReceived() <-chan struct{} {
	if p == nil {
		return nil
	}
	return p.ackReady
}

func (p *durableCompletionProxy) outcome() (pkgchannel.EgressOutcome, bool) {
	if p == nil {
		return "", false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.ack, p.hasAck
}

func (p *durableCompletionProxy) ForwardAck(ctx context.Context) error {
	if p == nil {
		return nil
	}
	select {
	case <-p.ackReady:
	case <-ctx.Done():
		return ctx.Err()
	}
	p.mu.Lock()
	if p.forwarded {
		err := p.settleErr
		p.mu.Unlock()
		return err
	}
	p.forwarded = true
	source, outcome := p.source, p.ack
	p.mu.Unlock()
	if source == nil {
		p.finishForward(nil)
		return nil
	}
	if err := source.Ack(ctx, outcome); err != nil {
		p.finishForward(err)
		return err
	}
	sourceDone := source.Done()
	if sourceDone == nil {
		p.finishForward(nil)
		return nil
	}
	select {
	case <-sourceDone:
		p.finishForward(nil)
		return nil
	case <-ctx.Done():
		p.finishForward(ctx.Err())
		return ctx.Err()
	}
}

func (p *durableCompletionProxy) finishForward(err error) {
	if p == nil {
		return
	}
	p.mu.Lock()
	if p.settleErr == nil {
		p.settleErr = err
	}
	p.mu.Unlock()
	p.doneOnce.Do(func() { close(p.done) })
}

// DurableIngress is the process-independent channel admission and consumer.
// Admission commits the canonical envelope and quotas before returning a
// stream; Run claims the oldest head, links an AgentRun before model work, and
// releases quota only after the adapter's completion Ack.
type DurableIngress struct {
	db      *pgxpool.Pool
	coord   *Coordinator
	owner   string
	lease   time.Duration
	poll    time.Duration
	log     *slog.Logger
	waitMu  sync.Mutex
	waiters map[string]*durableIngressWaiter
	lifeMu  sync.Mutex
	active  int
	idle    chan struct{}
	paused  bool
}

const durableIngressWorkerCount = 4

func NewDurableIngress(db *pgxpool.Pool, owner string) *DurableIngress {
	if owner == "" {
		owner = uuid.Must(uuid.NewV7()).String()
	}
	idle := make(chan struct{})
	close(idle)
	return &DurableIngress{
		db: db, owner: owner, lease: 30 * time.Second, poll: 250 * time.Millisecond,
		log: slog.Default(), waiters: make(map[string]*durableIngressWaiter), idle: idle,
	}
}

// Quiesce stops new FIFO claims while allowing already accepted items to
// finish their model and adapter completion tails. The gateway calls WaitIdle
// before canceling the worker context during graceful shutdown.
func (d *DurableIngress) Quiesce() {
	if d == nil {
		return
	}
	d.lifeMu.Lock()
	d.paused = true
	d.lifeMu.Unlock()
}

// WaitIdle waits until all processOne calls that passed the quiesce boundary
// have returned. It does not cancel or shorten their contexts.
func (d *DurableIngress) WaitIdle(ctx context.Context) error {
	if d == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	d.lifeMu.Lock()
	idle := d.idle
	if idle == nil {
		idle = make(chan struct{})
		close(idle)
		d.idle = idle
	}
	d.lifeMu.Unlock()
	select {
	case <-idle:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (d *DurableIngress) beginProcess() bool {
	d.lifeMu.Lock()
	defer d.lifeMu.Unlock()
	if d.paused {
		return false
	}
	if d.active == 0 {
		d.idle = make(chan struct{})
	}
	d.active++
	return true
}

func (d *DurableIngress) endProcess() {
	d.lifeMu.Lock()
	defer d.lifeMu.Unlock()
	if d.active == 0 {
		return
	}
	d.active--
	if d.active == 0 {
		if d.idle == nil {
			d.idle = make(chan struct{})
		}
		select {
		case <-d.idle:
		default:
			close(d.idle)
		}
	}
}

func (d *DurableIngress) BindCoordinator(c *Coordinator) {
	d.coord = c
}

func (d *DurableIngress) putWaiter(id string, w *durableIngressWaiter) {
	d.waitMu.Lock()
	d.waiters[id] = w
	d.waitMu.Unlock()
}

func (d *DurableIngress) takeWaiter(id string) *durableIngressWaiter {
	d.waitMu.Lock()
	w := d.waiters[id]
	delete(d.waiters, id)
	d.waitMu.Unlock()
	return w
}

// Admit persists one direct channel message and returns a proxy stream. The
// proxy stays open until the background consumer runs the item and the adapter
// acknowledges final egress; commands remain on the synchronous path until
// their control barrier is moved to the FIFO explicitly.
func (d *DurableIngress) Admit(ctx context.Context, msg pkgchannel.IncomingMessage, command, args string) (string, bool, *pkgchannel.ChatStream, error) {
	if d == nil || d.db == nil || d.coord == nil {
		return "", false, nil, errors.New("durable ingress is not configured")
	}
	command = strings.TrimSpace(command)
	if strings.EqualFold(command, newSessionCommand) && !msg.IsGroup {
		return d.admitNew(ctx, msg, command, args)
	}
	if command != "" || msg.IsGroup {
		return d.coord.handleIncomingDirect(ctx, msg, command, args)
	}
	canonical, owner, principalID, err := d.canonicalize(ctx, msg)
	if err != nil {
		return "", false, nil, err
	}
	msg.Content = canonical
	capability, err := d.prepareReplyCapability(msg.ReplyCapability)
	if err != nil {
		return "", false, nil, err
	}
	// ReplyCapability is ingress-only and has json:"-" as a second line of
	// defence. Clear it before retaining the message in the in-process waiter.
	msg.ReplyCapability = nil
	blocks, err := ai.MarshalContentBlocks(canonical)
	if err != nil {
		return "", false, nil, fmt.Errorf("marshal durable channel content: %w", err)
	}
	// IncomingMessage.Content is an interface slice and is not the durable
	// wire representation. Keep only the canonical top-level Content bytes;
	// processOne decodes those bytes before dispatch. Leaving the interface
	// slice populated makes encoding/json emit a second object that cannot be
	// decoded back into ai.ContentBlock.
	msg.Content = nil
	payload, err := json.Marshal(durableIngressPayload{
		Message: msg, Command: command, Args: args, Content: blocks,
		ReplyCapabilityRef: capabilityID(capability),
	})
	if err != nil {
		return "", false, nil, fmt.Errorf("marshal durable channel envelope: %w", err)
	}
	channelID := msg.ChannelID
	if channelID == "" {
		channelID = msg.Platform
	}
	_, chatKey := messageDeliveryCoordinates(msg)
	if principalID == "" {
		return "", false, nil, errors.New("durable channel principal is empty")
	}
	source := "delivery:" + uuid.Must(uuid.NewV7()).String()
	if msg.MessageID != "" {
		source = "message:" + msg.MessageID
	}
	itemID := uuid.Must(uuid.NewV7()).String()
	proxy := newDurableCompletionProxy()
	w := &durableIngressWaiter{events: make(chan pkgchannel.Event, 100), ctx: ctx}
	w.stream = &pkgchannel.ChatStream{Events: w.events, Completion: proxy}
	d.putWaiter(itemID, w)
	item, inserted, err := d.enqueue(ctx, itemID, source, channelID, msg.Platform, chatKey, msg.ThreadID, principalID, payload, canonical, owner, capability, "", "chat", "", "", nil)
	if err != nil {
		d.takeWaiter(itemID)
		return "", false, nil, err
	}
	if !inserted {
		d.takeWaiter(itemID)
		if item.ID != itemID {
			return "Message already accepted.", true, nil, nil
		}
	}
	return "", false, w.stream, nil
}

// admitNew turns the destructive /new command into a FIFO item. The current
// session is captured before the item is committed and carried as a compare
// value; the consumer rotates only that session, so a restart or a duplicate
// cannot archive the successor created by an earlier command.
func (d *DurableIngress) admitNew(ctx context.Context, msg pkgchannel.IncomingMessage, command, args string) (string, bool, *pkgchannel.ChatStream, error) {
	if msg.MessageID == "" {
		return pkgchannel.NewSessionUnverifiableMessage, true, nil, nil
	}
	rc, err := d.coord.resolve(ctx, msg)
	if err != nil {
		return "", false, nil, err
	}
	allowed, err := d.coord.channelPluginAllowed(ctx, rc)
	if err != nil {
		return "", false, nil, err
	}
	if !allowed {
		return "", false, nil, errChannelPluginDisabled
	}
	if rc.GuestID != "" && !d.coord.guestLimiter.allow(rc.GuestID, rc.GuestMessageLimitPerMinute) {
		return "Guest message rate limit exceeded. Try again in a minute.", true, nil, nil
	}
	current, err := rc.CurrentSessionForRotation(ctx)
	if err != nil {
		return "", false, nil, err
	}
	capability, err := d.prepareReplyCapability(msg.ReplyCapability)
	if err != nil {
		return "", false, nil, err
	}
	msg.ReplyCapability = nil
	content := ai.CloneContentBlocks(msg.Content)
	blocks, err := ai.MarshalContentBlocks(content)
	if err != nil {
		return "", false, nil, fmt.Errorf("marshal durable /new content: %w", err)
	}
	owner, ownerErr := sessionmedia.SessionOwner(rc.User.ID, rc.GroupID)
	principalID := rc.GuestID
	if ownerErr == nil {
		principalID = owner.ID.String()
	}
	if principalID == "" {
		return "", false, nil, errors.New("durable /new principal is empty")
	}
	// See Admit: the top-level canonical Content field is the only durable
	// representation. /new's command barrier also reconstructs Message.Content
	// after claiming the item.
	msg.Content = nil
	channelID, chatKey := messageDeliveryCoordinates(msg)
	source := "message:" + msg.MessageID
	itemID := uuid.Must(uuid.NewV7()).String()
	receipt := &durableCommandReceipt{
		channelID: channelID, chatKey: chatKey, messageID: msg.MessageID,
		command: command, binding: rc.queueKey(),
	}
	payload, err := json.Marshal(durableIngressPayload{
		Message: msg, Command: command, Args: args, Content: blocks,
		ReplyCapabilityRef: capabilityID(capability),
	})
	if err != nil {
		return "", false, nil, fmt.Errorf("marshal durable /new envelope: %w", err)
	}
	proxy := newDurableCompletionProxy()
	w := &durableIngressWaiter{events: make(chan pkgchannel.Event, 100), ctx: ctx}
	w.stream = &pkgchannel.ChatStream{Events: w.events, Completion: proxy}
	d.putWaiter(itemID, w)
	_, inserted, err := d.enqueue(ctx, itemID, source, channelID, msg.Platform, chatKey, msg.ThreadID, principalID, payload, content, owner, capability, rc.AgentID, command, args, current.ID, receipt)
	if err != nil {
		d.takeWaiter(itemID)
		if errors.Is(err, errDurableCommandAlreadyHandled) {
			return pkgchannel.SessionAlreadyResetMessage, true, nil, nil
		}
		return "", false, nil, err
	}
	if !inserted {
		d.takeWaiter(itemID)
		return pkgchannel.SessionAlreadyResetMessage, true, nil, nil
	}
	return "", false, w.stream, nil
}

func capabilityID(capability *durableReplyCapabilityRef) string {
	if capability == nil {
		return ""
	}
	return capability.ID
}

func (d *DurableIngress) prepareReplyCapability(capability *pkgchannel.ReplyCapability) (*durableReplyCapabilityRef, error) {
	if capability == nil {
		return nil, nil
	}
	if d == nil || d.coord == nil || d.coord.vaultSvc == nil {
		return nil, errors.New("durable reply capability encryption is unavailable")
	}
	if strings.TrimSpace(capability.Kind) == "" || strings.TrimSpace(capability.Secret) == "" || !capability.ExpiresAt.After(time.Now().UTC()) {
		return nil, errors.New("invalid or expired durable reply capability")
	}
	if len(capability.Kind) > maxDurableReplyCapabilityKindBytes {
		return nil, fmt.Errorf("durable reply capability kind exceeds %d bytes", maxDurableReplyCapabilityKindBytes)
	}
	if len(capability.Secret) > maxDurableReplyCapabilitySecretBytes {
		return nil, fmt.Errorf("durable reply capability secret exceeds %d bytes", maxDurableReplyCapabilitySecretBytes)
	}
	ciphertext, err := d.coord.vaultSvc.EncryptSystem(capability.Secret)
	if err != nil {
		return nil, fmt.Errorf("encrypt durable reply capability: %w", err)
	}
	if len(ciphertext) > maxDurableReplyCapabilityCipherBytes {
		return nil, fmt.Errorf("durable reply capability ciphertext exceeds %d bytes", maxDurableReplyCapabilityCipherBytes)
	}
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("mint durable reply capability id: %w", err)
	}
	return &durableReplyCapabilityRef{
		ID: id.String(), Kind: capability.Kind, Ciphertext: ciphertext,
		ExpiresAt: capability.ExpiresAt.UTC(),
	}, nil
}

func (d *DurableIngress) canonicalize(ctx context.Context, msg pkgchannel.IncomingMessage) ([]ai.ContentBlock, sessionmedia.Owner, string, error) {
	blocks := ai.CloneContentBlocks(msg.Content)
	rc, err := resolveAttachmentPrincipal(ctx, d.coord.store, d.coord.auth, d.coord.agentAccess, d.coord.groupResolver, d.coord.guests, msg.Platform, msg.ChannelID, msg.SenderID, msg.SenderIDs, msg.SenderName, msg.ChatID, msg.ThreadID, msg.IsGroup, d.coord.guestPolicy)
	if err != nil {
		return nil, sessionmedia.Owner{}, "", fmt.Errorf("resolve durable media principal: %w", err)
	}
	principalID := rc.GuestID
	owner, ownerErr := sessionmedia.SessionOwner(rc.User.ID, rc.GroupID)
	if ownerErr == nil {
		principalID = owner.ID.String()
	}
	if !ai.HasImage(blocks) {
		return blocks, owner, principalID, nil
	}
	if ownerErr != nil || d.coord.sessionImages == nil {
		return nil, sessionmedia.Owner{}, "", errors.New("canonical media persistence unavailable")
	}
	persisted, err := d.coord.sessionImages.Persist(ctx, owner, blocks)
	if err != nil {
		return nil, sessionmedia.Owner{}, "", fmt.Errorf("persist durable channel media: %w", err)
	}
	return persisted, owner, principalID, nil
}

func (d *DurableIngress) enqueue(ctx context.Context, itemID, source, channelID, platform, chatKey, threadKey, principalID string, payload []byte, blocks []ai.ContentBlock, owner sessionmedia.Owner, capability *durableReplyCapabilityRef, agentID, command, args, expectedSessionID string, receipt *durableCommandReceipt) (sqlc.ChannelFifoItem, bool, error) {
	tx, err := d.db.Begin(ctx)
	if err != nil {
		return sqlc.ChannelFifoItem{}, false, fmt.Errorf("begin durable channel admission: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := sqlc.New(tx)
	// /new is admitted only for a private chat, but guests still use the same
	// sender-scoped durable lane as ordinary direct messages.
	principalKind := "sender"
	if err := q.EnsureChannelBinding(ctx, sqlc.EnsureChannelBindingParams{ID: uuid.Must(uuid.NewV7()).String(), ChannelID: channelID, Platform: platform, ChatKey: chatKey, ThreadKey: threadKey, PrincipalKind: principalKind, PrincipalID: principalID, AgentID: agentID}); err != nil {
		return sqlc.ChannelFifoItem{}, false, fmt.Errorf("ensure durable channel binding: %w", err)
	}
	binding, err := q.GetChannelBindingByKey(ctx, sqlc.GetChannelBindingByKeyParams{ChannelID: channelID, Platform: platform, ChatKey: chatKey, ThreadKey: threadKey})
	if err != nil {
		return sqlc.ChannelFifoItem{}, false, err
	}
	if existing, err := q.GetChannelFIFOItemBySource(ctx, sqlc.GetChannelFIFOItemBySourceParams{BindingID: binding.ID, SourceKey: source}); err == nil {
		if receipt != nil {
			rows, receiptErr := q.CreateChatCommandReceipt(ctx, sqlc.CreateChatCommandReceiptParams{
				ChannelID: receipt.channelID, ChatKey: receipt.chatKey, MessageID: receipt.messageID,
				Command: receipt.command, Binding: receipt.binding,
			})
			if receiptErr != nil {
				return sqlc.ChannelFifoItem{}, false, fmt.Errorf("backfill durable command receipt: %w", receiptErr)
			}
			if rows == 1 {
				linked, linkErr := q.LinkChatCommandReceipt(ctx, sqlc.LinkChatCommandReceiptParams{
					FifoItemID: existing.ID, ExpectedSessionID: existing.ExpectedSessionID,
					BindingRevision: existing.ExpectedBindingRevision, ChannelID: receipt.channelID,
					ChatKey: receipt.chatKey, MessageID: receipt.messageID,
				})
				if linkErr != nil || linked != 1 {
					if linkErr != nil {
						return sqlc.ChannelFifoItem{}, false, fmt.Errorf("backfill durable command receipt link: %w", linkErr)
					}
					return sqlc.ChannelFifoItem{}, false, errors.New("durable command receipt backfill lost its row")
				}
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return sqlc.ChannelFifoItem{}, false, err
		}
		return existing, false, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return sqlc.ChannelFifoItem{}, false, err
	}
	if _, err := q.EnsureChannelDeploymentQuota(ctx, sqlc.EnsureChannelDeploymentQuotaParams{ChannelID: channelDeploymentQuotaKey, MaxRows: channelDeploymentDefaultRows, MaxBytes: channelDeploymentDefaultBytes}); err != nil {
		return sqlc.ChannelFifoItem{}, false, err
	}
	principalKey := "sender:" + principalID
	if _, err := q.EnsureChannelPrincipalQuota(ctx, sqlc.EnsureChannelPrincipalQuotaParams{PrincipalKey: principalKey, PrincipalKind: "sender", PrincipalID: principalID, MaxRows: channelPrincipalDefaultRows, MaxBytes: channelPrincipalDefaultBytes}); err != nil {
		return sqlc.ChannelFifoItem{}, false, err
	}
	if _, err := q.GetChannelDeploymentQuotaForUpdate(ctx, channelDeploymentQuotaKey); err != nil {
		return sqlc.ChannelFifoItem{}, false, err
	}
	if _, err := q.GetChannelPrincipalQuotaForUpdate(ctx, principalKey); err != nil {
		return sqlc.ChannelFifoItem{}, false, err
	}
	binding, err = q.GetChannelBindingForUpdate(ctx, binding.ID)
	if err != nil {
		return sqlc.ChannelFifoItem{}, false, err
	}
	if binding.State != "active" {
		return sqlc.ChannelFifoItem{}, false, fmt.Errorf("channel binding is %s", binding.State)
	}
	if receipt != nil {
		rows, err := q.CreateChatCommandReceipt(ctx, sqlc.CreateChatCommandReceiptParams{
			ChannelID: receipt.channelID, ChatKey: receipt.chatKey, MessageID: receipt.messageID,
			Command: receipt.command, Binding: receipt.binding,
		})
		if err != nil {
			return sqlc.ChannelFifoItem{}, false, fmt.Errorf("claim durable command receipt: %w", err)
		}
		if rows == 0 {
			return sqlc.ChannelFifoItem{}, false, errDurableCommandAlreadyHandled
		}
	}
	media, err := d.mediaForOwner(ctx, q, owner, blocks)
	if err != nil {
		return sqlc.ChannelFifoItem{}, false, err
	}
	var mediaBytes int64
	for _, m := range media {
		if m.SizeBytes < 0 || mediaBytes > math.MaxInt64-m.SizeBytes {
			return sqlc.ChannelFifoItem{}, false, errors.New("durable channel media byte cost overflow")
		}
		mediaBytes += m.SizeBytes
	}
	payloadBytes := int64(len(payload))
	capabilityBytes := int64(0)
	if capability != nil {
		capabilityBytes = int64(len(capability.Ciphertext))
	}
	if payloadBytes <= 0 || payloadBytes > math.MaxInt64-mediaBytes || capabilityBytes > math.MaxInt64-payloadBytes-mediaBytes {
		return sqlc.ChannelFifoItem{}, false, errors.New("durable channel payload byte cost overflow")
	}
	cost := payloadBytes + mediaBytes + capabilityBytes
	for _, reserve := range []func() (int64, error){
		func() (int64, error) {
			return q.ReserveChannelDeploymentQuota(ctx, sqlc.ReserveChannelDeploymentQuotaParams{ChannelID: channelDeploymentQuotaKey, ByteCost: cost})
		},
		func() (int64, error) {
			return q.ReserveChannelPrincipalQuota(ctx, sqlc.ReserveChannelPrincipalQuotaParams{PrincipalKey: principalKey, ByteCost: cost})
		},
		func() (int64, error) {
			return q.ReserveChannelBindingQuota(ctx, sqlc.ReserveChannelBindingQuotaParams{BindingID: binding.ID, ByteCost: cost})
		},
	} {
		rows, err := reserve()
		if err != nil {
			return sqlc.ChannelFifoItem{}, false, err
		}
		if rows != 1 {
			return sqlc.ChannelFifoItem{}, false, ErrChannelFIFOQuotaExceeded
		}
	}
	seq, err := q.AllocateChannelBindingSeq(ctx, binding.ID)
	if err != nil {
		return sqlc.ChannelFifoItem{}, false, err
	}
	var expectedBindingRevision any
	if expectedSessionID != "" {
		// Only /new carries a compare-and-rotate barrier. Ordinary messages
		// must bind to the lane revision that is current when they reach the
		// head, otherwise a queued message is poisoned by a prior /new.
		expectedBindingRevision = binding.Revision
	}
	item, err := q.InsertChannelFIFOItem(ctx, sqlc.InsertChannelFIFOItemParams{
		ID: itemID, BindingID: binding.ID, PrincipalKey: principalKey, Seq: int64(seq), SourceKey: source,
		SchemaVersion: 1, Payload: payload, PayloadBytes: payloadBytes,
		MediaBytes: mediaBytes, CapabilityBytes: capabilityBytes, ByteCost: cost, Command: command,
		Args:              args,
		ExpectedSessionID: expectedSessionID, ExpectedBindingRevision: expectedBindingRevision,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		existing, getErr := q.GetChannelFIFOItemBySource(ctx, sqlc.GetChannelFIFOItemBySourceParams{BindingID: binding.ID, SourceKey: source})
		if getErr != nil {
			return sqlc.ChannelFifoItem{}, false, getErr
		}
		// The reservation updates above belong to this losing insert attempt.
		// Leave the transaction uncommitted so the deferred rollback removes
		// them before returning the already accepted item.
		return existing, false, nil
	}
	if err != nil {
		return sqlc.ChannelFifoItem{}, false, err
	}
	if receipt != nil {
		rows, err := q.LinkChatCommandReceipt(ctx, sqlc.LinkChatCommandReceiptParams{
			FifoItemID: item.ID, ExpectedSessionID: expectedSessionID, BindingRevision: binding.Revision,
			ChannelID: receipt.channelID, ChatKey: receipt.chatKey, MessageID: receipt.messageID,
		})
		if err != nil {
			return sqlc.ChannelFifoItem{}, false, fmt.Errorf("link durable command receipt: %w", err)
		}
		if rows != 1 {
			return sqlc.ChannelFifoItem{}, false, errors.New("durable command receipt was linked concurrently")
		}
	}
	if capability != nil {
		if _, err := q.CreateChannelReplyCapability(ctx, sqlc.CreateChannelReplyCapabilityParams{
			ID: capability.ID, ChannelID: channelID, Kind: capability.Kind,
			Ciphertext: capability.Ciphertext, ExpiresAt: capability.ExpiresAt,
			FifoItemID: item.ID,
		}); err != nil {
			return sqlc.ChannelFifoItem{}, false, fmt.Errorf("persist durable reply capability: %w", err)
		}
	}
	for _, m := range media {
		if _, err := q.AddChannelFIFOMedia(ctx, sqlc.AddChannelFIFOMediaParams{
			ItemID: item.ID, MediaID: m.ID, MimeType: m.MimeType, SizeBytes: m.SizeBytes,
		}); err != nil {
			return sqlc.ChannelFifoItem{}, false, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return sqlc.ChannelFifoItem{}, false, err
	}
	return item, true, nil
}

func ownerKind(owner sessionmedia.Owner) pgtype.Text {
	return pgtype.Text{String: string(owner.Kind), Valid: owner.Valid()}
}

func (d *DurableIngress) mediaForOwner(ctx context.Context, q *sqlc.Queries, owner sessionmedia.Owner, blocks []ai.ContentBlock) ([]sqlc.CtxMedium, error) {
	if !owner.Valid() {
		for _, block := range blocks {
			if ref, ok := block.(ai.ImageRefContent); ok && ref.MediaID != "" {
				return nil, errors.New("durable channel media reference has no owner")
			}
		}
		return nil, nil
	}
	mediaIDs := make([]string, 0)
	seen := make(map[string]struct{})
	for _, block := range blocks {
		ref, ok := block.(ai.ImageRefContent)
		if !ok || ref.MediaID == "" {
			continue
		}
		if _, exists := seen[ref.MediaID]; exists {
			continue
		}
		seen[ref.MediaID] = struct{}{}
		mediaIDs = append(mediaIDs, ref.MediaID)
	}
	if len(mediaIDs) == 0 {
		return nil, nil
	}
	media, err := q.ListMediaByIDsForOwner(ctx, sqlc.ListMediaByIDsForOwnerParams{
		OwnerKind: ownerKind(owner),
		OwnerID:   pgtype.Text{String: owner.ID.String(), Valid: true},
		MediaIds:  mediaIDs,
	})
	if err != nil {
		return nil, fmt.Errorf("list durable channel media: %w", err)
	}
	if len(media) != len(mediaIDs) {
		return nil, errors.New("durable channel media reference is missing or foreign")
	}
	return media, nil
}

// Run continuously recovers unlinked claims and executes durable heads. The
// composition root starts it beside GroupDispatcher.Run and cancels it during
// ingress drain. SQL head eligibility provides per-binding ordering while the
// bounded workers let unrelated bindings make progress concurrently.
func (d *DurableIngress) Run(ctx context.Context) error {
	if d == nil || d.db == nil || d.coord == nil {
		return errors.New("durable ingress is not configured")
	}
	var workers sync.WaitGroup
	workers.Go(func() { d.runReaper(ctx) })
	for range durableIngressWorkerCount {
		workers.Go(func() { d.runConsumer(ctx) })
	}
	workers.Wait()
	return ctx.Err()
}

func (d *DurableIngress) runConsumer(ctx context.Context) {
	poll := d.poll
	if poll <= 0 {
		poll = 250 * time.Millisecond
	}
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		if err := d.processOne(ctx); err != nil && ctx.Err() == nil {
			d.log.Warn("durable channel FIFO item failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (d *DurableIngress) runReaper(ctx context.Context) {
	poll := d.poll
	if poll <= 0 {
		poll = 250 * time.Millisecond
	}
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		if err := d.reap(ctx); err != nil && ctx.Err() == nil {
			d.log.Warn("durable channel FIFO recovery failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (d *DurableIngress) reap(ctx context.Context) error {
	q := sqlc.New(d.db)
	if _, err := q.ReapExpiredUnlinkedChannelFIFOItems(ctx); err != nil {
		return err
	}
	linked, err := q.ListLinkedChannelFIFOItemsNeedingRunRecovery(ctx)
	if err != nil {
		return err
	}
	for _, row := range linked {
		if row.RunStatus == "completed" && row.RunCompletionOutcome == string(pkgchannel.EgressDelivered) {
			if err := d.completeRecovered(ctx, row.ID, row.BindingID, row.PrincipalKey, row.ByteCost); err != nil {
				return err
			}
			continue
		}
		if _, err := q.BlockLinkedChannelFIFOItem(ctx, sqlc.BlockLinkedChannelFIFOItemParams{
			ID: row.ID, ErrorCode: "run_terminal", ErrorDetail: row.RunStatus + ":" + row.RunCompletionOutcome,
		}); err != nil {
			return err
		}
	}
	_, err = q.SweepExpiredChannelReplyCapabilities(ctx)
	return err
}

func (d *DurableIngress) completeRecovered(ctx context.Context, itemID, bindingID, principalKey string, expectedCost int64) error {
	tx, err := d.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := sqlc.New(tx)
	if principalKey == "" {
		return errors.New("recovered channel FIFO item has no quota principal")
	}
	if _, err := q.GetChannelDeploymentQuotaForUpdate(ctx, channelDeploymentQuotaKey); err != nil {
		return err
	}
	if _, err := q.GetChannelPrincipalQuotaForUpdate(ctx, principalKey); err != nil {
		return err
	}
	if _, err := q.GetChannelBindingForUpdate(ctx, bindingID); err != nil {
		return err
	}
	item, err := q.CompleteLinkedChannelFIFOItem(ctx, itemID)
	if err != nil {
		return err
	}
	if item.ByteCost != expectedCost {
		return errors.New("recovered channel FIFO byte cost changed")
	}
	if item.PrincipalKey != principalKey {
		return errors.New("recovered channel FIFO quota principal changed")
	}
	rows, err := q.ReleaseChannelDeploymentQuota(ctx, sqlc.ReleaseChannelDeploymentQuotaParams{ChannelID: channelDeploymentQuotaKey, ByteCost: item.ByteCost})
	if err != nil || rows != 1 {
		if err != nil {
			return err
		}
		return errors.New("recovered deployment quota release lost its row")
	}
	rows, err = q.ReleaseChannelPrincipalQuota(ctx, sqlc.ReleaseChannelPrincipalQuotaParams{PrincipalKey: principalKey, ByteCost: item.ByteCost})
	if err != nil || rows != 1 {
		if err != nil {
			return err
		}
		return errors.New("recovered principal quota release lost its row")
	}
	rows, err = q.ReleaseChannelBindingQuota(ctx, sqlc.ReleaseChannelBindingQuotaParams{BindingID: bindingID, ByteCost: item.ByteCost})
	if err != nil || rows != 1 {
		if err != nil {
			return err
		}
		return errors.New("recovered binding quota release lost its row")
	}
	if err := cleanupTerminalFIFOItem(ctx, q, itemID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// processGroupFIFO runs the group dispatcher inline. The FIFO-owned completion
// proxy captures the publisher's outcome without blocking, so HandleFIFO can
// publish from inside this call; the FIFO item settles first and only then is
// the outcome forwarded to the source AgentRun.
func (d *DurableIngress) processGroupFIFO(ctx, dispatchCtx context.Context, item sqlc.ChannelFifoItem) error {
	proxy := newDurableCompletionProxy()
	handleErr := d.coord.groupDispatcher.HandleFIFO(dispatchCtx, item, proxy)
	if handleErr != nil {
		// A captured delivered outcome is not trustworthy when the dispatcher
		// returned an error before the FIFO settled; downgrade it to unknown.
		// A proven failed/discarded outcome is truthful and survives.
		if outcome, ok := proxy.outcome(); !ok || outcome == pkgchannel.EgressDelivered {
			_ = proxy.forceAck(pkgchannel.EgressUnknown)
		}
		retryErr := d.retry(ctx, item, "group_dispatch_failed", handleErr.Error(), proxy.hasSource())
		forwardErr := d.forwardGroupFIFOProxy(ctx, proxy)
		return errors.Join(retryErr, forwardErr, handleErr)
	}
	if !proxy.hasSource() {
		// No turn ran under this attempt: the mirrored ledger was already
		// terminal, so its recorded business outcome owns the release.
		return d.complete(ctx, item, "", false)
	}
	outcome, ok := proxy.outcome()
	if !ok || outcome == pkgchannel.EgressUnknown {
		_ = proxy.forceAck(pkgchannel.EgressUnknown)
		settleErr := d.retry(ctx, item, "egress_unknown", "group publisher outcome was not confirmed", true)
		forwardErr := d.forwardGroupFIFOProxy(ctx, proxy)
		return errors.Join(settleErr, forwardErr)
	}
	settleErr := d.complete(ctx, item, "", false)
	if settleErr != nil {
		// The publisher's outcome is still held by the proxy. A failed FIFO
		// terminalization means the source boundary is unknown, so never
		// forward delivered while the item remains claimable.
		settleErr = errors.Join(settleErr, proxy.forceAck(pkgchannel.EgressUnknown))
	}
	forwardErr := d.forwardGroupFIFOProxy(ctx, proxy)
	return errors.Join(settleErr, forwardErr)
}

func (d *DurableIngress) forwardGroupFIFOProxy(ctx context.Context, proxy *durableCompletionProxy) error {
	if proxy == nil || !proxy.hasSource() {
		return nil
	}
	ackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return proxy.ForwardAck(ackCtx)
}

func (d *DurableIngress) processOne(ctx context.Context) error {
	if !d.beginProcess() {
		return nil
	}
	defer d.endProcess()
	item, err := sqlc.New(d.db).ClaimNextChannelFIFOItem(ctx, sqlc.ClaimNextChannelFIFOItemParams{LeaseOwner: d.owner, LeaseSeconds: d.lease.Seconds()})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	linked := false
	dispatchCtx := d.admissionLinkContext(ctx, item, &linked)
	if item.Command == "group_responder" {
		if d.coord == nil || d.coord.groupDispatcher == nil {
			return d.retry(ctx, item, "group_dispatcher_unavailable", "group FIFO dispatcher is unavailable", true)
		}
		var payload groupResponderPayload
		if err := json.Unmarshal(item.Payload, &payload); err != nil {
			return d.retry(ctx, item, "invalid_group_payload", err.Error(), true)
		}
		if err := validateGroupResponderPayload(payload, item.Args); err != nil {
			return d.retry(ctx, item, "invalid_group_payload", err.Error(), true)
		}
		return d.processGroupFIFO(ctx, dispatchCtx, item)
	}
	var envelope durableIngressPayload
	if err := json.Unmarshal(item.Payload, &envelope); err != nil {
		return d.retry(ctx, item, "invalid_payload", err.Error(), true)
	}
	var streamText string
	var handled bool
	var stream *pkgchannel.ChatStream
	if strings.EqualFold(strings.TrimSpace(item.Command), newSessionCommand) {
		if d.coord == nil {
			return d.retry(ctx, item, "coordinator_unavailable", "channel coordinator is unavailable", true)
		}
		rc, resolveErr := d.coord.resolve(dispatchCtx, envelope.Message)
		if resolveErr != nil {
			return d.retry(ctx, item, "dispatch_failed", resolveErr.Error(), false)
		}
		allowed, allowErr := d.coord.channelPluginAllowed(dispatchCtx, rc)
		if allowErr != nil {
			return d.retry(ctx, item, "dispatch_failed", allowErr.Error(), false)
		}
		if !allowed {
			return d.retry(ctx, item, "channel_plugin_disabled", errChannelPluginDisabled.Error(), true)
		}
		expectedSessionID := ""
		if item.ExpectedSessionID.Valid {
			expectedSessionID = item.ExpectedSessionID.String
		}
		streamText = rotateChatSessionExpected(dispatchCtx, rc, expectedSessionID, d.coord.queue, func(authCtx context.Context) error {
			return rc.AuthorizeUse(authCtx, d.coord.agentAccess)
		})
		handled = true
	} else {
		blocks, unmarshalErr := ai.UnmarshalContentBlocks(envelope.Content)
		if unmarshalErr != nil {
			return d.retry(ctx, item, "invalid_content", unmarshalErr.Error(), true)
		}
		envelope.Message.Content = blocks
		streamText, handled, stream, err = d.coord.handleIncomingDirect(dispatchCtx, envelope.Message, envelope.Command, envelope.Args)
		if err != nil {
			return d.retry(ctx, item, "dispatch_failed", err.Error(), errors.Is(err, errDurableSourcePrincipal))
		}
	}
	if handled {
		stream = &pkgchannel.ChatStream{Events: eventStream(pkgchannel.Event{Text: streamText})}
	}
	if stream == nil {
		return d.complete(ctx, item, "", handled)
	}
	if !handled && !linked {
		return d.retry(ctx, item, "missing_agent_run_link", "channel stream was created without an atomic AgentRun link", true)
	}
	w := d.takeWaiter(item.ID)
	if w == nil {
		return d.publishWithoutWaiter(ctx, item, envelope, stream)
	}
	proxy, _ := w.stream.Completion.(*durableCompletionProxy)
	if proxy != nil {
		proxy.Bind(stream.Completion)
	}
	waitCtx, stopWait := mergeDurableWaitContexts(ctx, w.ctx)
	defer stopWait()
	forwarding := true
	for forwarding {
		select {
		case event, ok := <-stream.Events:
			if !ok {
				forwarding = false
				continue
			}
			select {
			case w.events <- event:
			case <-waitCtx.Done():
				forwarding = false
				if proxy != nil {
					_ = proxy.captureAck(pkgchannel.EgressUnknown)
				}
			}
		case <-waitCtx.Done():
			forwarding = false
			if proxy != nil {
				_ = proxy.captureAck(pkgchannel.EgressUnknown)
			}
		}
	}
	if waitCtx.Err() != nil {
		go func() {
			for range stream.Events {
			}
		}()
	}
	close(w.events)
	if proxy == nil {
		select {
		case <-w.stream.CompletionDone():
		case <-ctx.Done():
			return ctx.Err()
		}
		return d.complete(ctx, item, "", false)
	}
	outcome := d.waitProxyAck(waitCtx, proxy)
	if outcome == pkgchannel.EgressUnknown {
		retryErr := d.retry(ctx, item, "egress_unknown", "adapter did not confirm durable egress", true)
		_ = proxy.forceAck(pkgchannel.EgressUnknown)
		ackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		forwardErr := proxy.ForwardAck(ackCtx)
		cancel()
		return errors.Join(retryErr, forwardErr)
	}
	if err := d.complete(ctx, item, "", false); err != nil {
		_ = proxy.forceAck(pkgchannel.EgressUnknown)
		ackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		forwardErr := proxy.ForwardAck(ackCtx)
		cancel()
		return errors.Join(err, forwardErr)
	}
	ackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return proxy.ForwardAck(ackCtx)
}

func (d *DurableIngress) admissionLinkContext(ctx context.Context, item sqlc.ChannelFifoItem, linked *bool) context.Context {
	return agentrun.WithAdmissionLink(withDurableIngressBypass(ctx), func(linkCtx context.Context, tx pgx.Tx, guard agentrun.Guard) error {
		if linked != nil {
			*linked = true
		}
		q := sqlc.New(tx)
		binding, err := q.GetChannelBindingForUpdate(linkCtx, item.BindingID)
		if err != nil {
			return fmt.Errorf("lock durable channel binding for AgentRun link: %w", err)
		}
		if item.ExpectedSessionID.Valid {
			if !item.ExpectedBindingRevision.Valid {
				return errors.New("durable /new FIFO item has no binding revision")
			}
			if binding.Revision != item.ExpectedBindingRevision.Int64 {
				return fmt.Errorf("durable channel binding revision changed: claimed=%d current=%d", item.ExpectedBindingRevision.Int64, binding.Revision)
			}
		}
		// Resolve the session again at dequeue time, but keep the admission
		// principal as the source of truth. A platform identity may have been
		// rebound between admission and execution; without this same-transaction
		// owner check the old payload would run in the new user's session.
		if err := validateDurableSourcePrincipal(linkCtx, q, guard.SessionID, item.PrincipalKey); err != nil {
			return err
		}
		rows, err := q.LinkChannelFIFOAgentRun(linkCtx, sqlc.LinkChannelFIFOAgentRunParams{
			RunID: guard.RunID, ID: item.ID, LeaseOwner: d.owner, Attempt: item.Attempt,
			SessionID:       pgtype.Text{String: guard.SessionID, Valid: guard.SessionID != ""},
			BindingRevision: binding.Revision,
		})
		if err != nil {
			return fmt.Errorf("link durable channel AgentRun: %w", err)
		}
		if rows != 1 {
			return errors.New("durable channel AgentRun link lost its claim")
		}
		return nil
	})
}

// validateDurableSourcePrincipal derives the owner from the durable
// conversation row selected by the runtime, then compares it with the
// principal captured when the FIFO item was admitted. It runs on the same
// transaction as LinkChannelFIFOAgentRun, so a mismatch rolls back the newly
// created AgentRun before any runner or model work can observe it.
func validateDurableSourcePrincipal(ctx context.Context, q *sqlc.Queries, sessionID, principalKey string) error {
	if q == nil || sessionID == "" || principalKey == "" {
		return fmt.Errorf("%w: missing durable source owner facts", errDurableSourcePrincipal)
	}
	facts, err := q.GetConversationForSessionAccess(ctx, sessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: session %q has no conversation", errDurableSourcePrincipal, sessionID)
	}
	if err != nil {
		return fmt.Errorf("%w: load session %q owner: %w", errDurableSourcePrincipal, sessionID, err)
	}
	current, err := conversationPrincipalKey(facts)
	if err != nil {
		return fmt.Errorf("%w: session %q owner facts: %w", errDurableSourcePrincipal, sessionID, err)
	}
	if current != principalKey {
		return fmt.Errorf("%w: item=%q session=%q", errDurableSourcePrincipal, principalKey, current)
	}
	return nil
}

func conversationPrincipalKey(facts sqlc.CtxConversation) (string, error) {
	if facts.GroupID.Valid && facts.GroupID.String != "" {
		if !facts.UserID.Valid || facts.UserID.String != facts.GroupID.String {
			return "", errors.New("group conversation owner is inconsistent")
		}
		return "group:" + facts.GroupID.String, nil
	}
	if facts.GuestID.Valid && facts.GuestID.String != "" {
		if !facts.UserID.Valid || facts.UserID.String != facts.GuestID.String {
			return "", errors.New("guest conversation owner is inconsistent")
		}
		return "sender:" + facts.GuestID.String, nil
	}
	if !facts.UserID.Valid || facts.UserID.String == "" {
		return "", errors.New("conversation has no user owner")
	}
	return "sender:" + facts.UserID.String, nil
}

// publishWithoutWaiter is the restart/replica path. An accepted FIFO item may
// outlive the listener that returned its proxy stream, so silently draining it
// would turn durable admission into message loss. Reconstruct the egress
// client from the channel row and opaque capability reference instead. The
// publisher reports its outcome through the proxy's capture-only Ack; this
// function stays the single owner that settles the FIFO item and then
// terminalizes the source AgentRun.
func (d *DurableIngress) publishWithoutWaiter(ctx context.Context, item sqlc.ChannelFifoItem, envelope durableIngressPayload, stream *pkgchannel.ChatStream) error {
	if stream == nil {
		return d.complete(ctx, item, "", false)
	}
	proxy := newDurableCompletionProxy()
	proxy.Bind(stream.Completion)
	settle := func(settleErr error) error {
		return errors.Join(settleErr, d.forwardProxyAck(ctx, proxy))
	}
	if d.coord == nil || d.coord.publisherReconstructor == nil || d.coord.store == nil {
		stream.Discard()
		_ = proxy.forceAck(pkgchannel.EgressUnknown)
		return settle(d.retry(ctx, item, "publisher_unavailable", "durable incoming publisher reconstruction is unavailable", true))
	}
	channelID := envelope.Message.ChannelID
	if channelID == "" {
		channelID = envelope.Message.Platform
	}
	configured, err := d.coord.store.GetChannel(ctx, channelID)
	if err != nil {
		stream.Discard()
		_ = proxy.forceAck(pkgchannel.EgressUnknown)
		return settle(d.retry(ctx, item, "publisher_config_failed", err.Error(), true))
	}
	publisher, err := d.coord.publisherReconstructor.ReconstructIncomingPublisher(ctx, configured, GroupOutboxEnvelope{
		LifecycleFeedback:  envelope.Message.LifecycleFeedback,
		ReplyCapabilityRef: envelope.ReplyCapabilityRef,
	})
	if err != nil || publisher == nil {
		stream.Discard()
		if err == nil {
			err = errors.New("durable incoming publisher reconstruction returned nil")
		}
		_ = proxy.forceAck(pkgchannel.EgressUnknown)
		return settle(d.retry(ctx, item, "publisher_reconstruct_failed", err.Error(), true))
	}
	publishStream := &pkgchannel.ChatStream{Events: stream.Events, SessionID: stream.SessionID, Completion: proxy}
	publishErr := publisher.PublishIncoming(ctx, pkgchannel.DurablePublishRequest{
		DeliveryID:        item.ID,
		Platform:          envelope.Message.Platform,
		ChannelID:         channelID,
		ChatID:            envelope.Message.ChatID,
		ThreadID:          envelope.Message.ThreadID,
		TargetID:          envelope.Message.SenderID,
		ReplyTo:           envelope.Message.ReplyTo,
		MessageID:         envelope.Message.MessageID,
		IsGroup:           envelope.Message.IsGroup,
		LifecycleFeedback: envelope.Message.LifecycleFeedback,
		Stream:            publishStream,
	})
	if publishErr != nil {
		// A returned error cannot prove whether the platform accepted the
		// bytes, so the outcome stays unknown regardless of any capture.
		_ = proxy.forceAck(pkgchannel.EgressUnknown)
		return settle(errors.Join(d.retry(ctx, item, "publisher_failed", publishErr.Error(), true), publishErr))
	}
	// A publisher that returned without acknowledging cannot prove the
	// external outcome. Keep the item blocked for explicit recovery.
	outcome, ok := proxy.outcome()
	if !ok || outcome == pkgchannel.EgressUnknown {
		_ = proxy.forceAck(pkgchannel.EgressUnknown)
		return settle(d.retry(ctx, item, "egress_unknown", "durable incoming publisher outcome was not confirmed", true))
	}
	settleErr := d.complete(ctx, item, "", false)
	if settleErr != nil {
		// A delivered acknowledgement cannot outrun durable FIFO settlement.
		settleErr = errors.Join(settleErr, proxy.forceAck(pkgchannel.EgressUnknown))
	}
	return settle(settleErr)
}

func (d *DurableIngress) forwardProxyAck(ctx context.Context, proxy *durableCompletionProxy) error {
	if proxy == nil {
		return nil
	}
	ackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return proxy.ForwardAck(ackCtx)
}

func mergeDurableWaitContexts(parent, waiter context.Context) (context.Context, func()) {
	if parent == nil {
		parent = context.Background()
	}
	if waiter == nil || waiter == parent {
		return parent, func() {}
	}
	merged, cancel := context.WithCancel(context.Background())
	stopParent := context.AfterFunc(parent, cancel)
	stopWaiter := context.AfterFunc(waiter, cancel)
	return merged, func() {
		stopParent()
		stopWaiter()
		cancel()
	}
}

// Cancellation records unknown only when the adapter has not reported an
// outcome. An adapter can cancel its context immediately after Ack returns;
// that must not overwrite the result it already reported.
func (d *DurableIngress) waitProxyAck(ctx context.Context, proxy *durableCompletionProxy) pkgchannel.EgressOutcome {
	if proxy == nil {
		return pkgchannel.EgressDelivered
	}
	select {
	case <-proxy.AckReceived():
	case <-ctx.Done():
		_ = proxy.captureAck(pkgchannel.EgressUnknown)
	}
	outcome, ok := proxy.outcome()
	if !ok {
		return pkgchannel.EgressUnknown
	}
	return outcome
}

func eventStream(event pkgchannel.Event) <-chan pkgchannel.Event {
	ch := make(chan pkgchannel.Event, 1)
	ch <- event
	close(ch)
	return ch
}

// retry returns a claimed item to the FIFO with a fixed short delay, for
// failure classes where an immediate second attempt is expected to help.
// RetryChannelFIFOItem re-reads run_id in the UPDATE. The claimed item is a
// snapshot and may predate the admission transaction that linked its
// AgentRun, so Go-side RunID checks cannot decide whether replay is safe.
func (d *DurableIngress) retry(ctx context.Context, item sqlc.ChannelFifoItem, code, detail string, block bool) error {
	_, err := sqlc.New(d.db).RetryChannelFIFOItem(ctx, sqlc.RetryChannelFIFOItemParams{ID: item.ID, LeaseOwner: d.owner, Block: block, NextAttemptAt: time.Now().UTC().Add(time.Second), ErrorCode: code, ErrorDetail: detail})
	return err
}

func (d *DurableIngress) complete(ctx context.Context, item sqlc.ChannelFifoItem, result string, handled bool) error {
	tx, err := d.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := sqlc.New(tx)
	// Admission locks deployment -> principal -> binding. Terminalization takes
	// the same order before touching the item, so concurrent lanes cannot form a
	// quota-release deadlock with a new admission.
	if item.PrincipalKey == "" {
		return errors.New("durable channel FIFO item has no quota principal")
	}
	if _, err := q.GetChannelDeploymentQuotaForUpdate(ctx, channelDeploymentQuotaKey); err != nil {
		return err
	}
	if _, err := q.GetChannelPrincipalQuotaForUpdate(ctx, item.PrincipalKey); err != nil {
		return err
	}
	lockedBinding, err := q.GetChannelBindingForUpdate(ctx, item.BindingID)
	if err != nil {
		return err
	}
	if lockedBinding.ChannelID == "" {
		return errors.New("durable channel binding has no channel identity")
	}
	updated, err := q.SetChannelFIFOResult(ctx, sqlc.SetChannelFIFOResultParams{ID: item.ID, LeaseOwner: d.owner, ResultText: result, ResultHandled: handled})
	if err != nil {
		return err
	}
	if updated != 1 {
		return errors.New("durable channel FIFO result lost its claim")
	}
	completed, err := q.CompleteChannelFIFOItem(ctx, sqlc.CompleteChannelFIFOItemParams{ID: item.ID, LeaseOwner: d.owner})
	if err != nil {
		return err
	}
	rows, err := q.ReleaseChannelDeploymentQuota(ctx, sqlc.ReleaseChannelDeploymentQuotaParams{ChannelID: channelDeploymentQuotaKey, ByteCost: completed.ByteCost})
	if err != nil {
		return err
	}
	if rows != 1 {
		return errors.New("durable channel deployment quota release lost its row")
	}
	rows, err = q.ReleaseChannelPrincipalQuota(ctx, sqlc.ReleaseChannelPrincipalQuotaParams{PrincipalKey: completed.PrincipalKey, ByteCost: completed.ByteCost})
	if err != nil {
		return err
	}
	if rows != 1 {
		return errors.New("durable channel principal quota release lost its row")
	}
	rows, err = q.ReleaseChannelBindingQuota(ctx, sqlc.ReleaseChannelBindingQuotaParams{BindingID: completed.BindingID, ByteCost: completed.ByteCost})
	if err != nil {
		return err
	}
	if rows != 1 {
		return errors.New("durable channel binding quota release lost its row")
	}
	if err := cleanupTerminalFIFOItem(ctx, q, completed.ID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
