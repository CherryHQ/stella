package session

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/memory"
)

// ErrNotFound is returned when a session cannot be located.
var ErrNotFound = errors.New("session not found")

// ErrForbidden is returned when a caller attempts to access a session they do not own.
var ErrForbidden = errors.New("session access forbidden")

// ErrWrongKind is returned when a resumed session has a different kind than required.
var ErrWrongKind = errors.New("session kind mismatch")

// ErrArchived is returned when an attempt is made to write to an archived session.
var ErrArchived = errors.New("session is archived")

// ErrStaleRotation is returned when a rotation's expected session is no longer
// the active one. It is the store-level sentinel so callers can match a stale
// rotation with one errors.Is check regardless of which layer detected it.
var ErrStaleRotation = memory.ErrStaleRotation

// ErrRotationOutcomeUnknown is returned when a rotation failed at commit
// acknowledgement and may or may not have been persisted. See
// memory.ErrRotationOutcomeUnknown.
var ErrRotationOutcomeUnknown = memory.ErrRotationOutcomeUnknown

// Registry is the sole owner of agent-session lifecycle.
// It creates, resumes, lists, and archives sessions; it also converts validated
// session records into memory operation scopes.
//
// Registry has no knowledge of LLM execution, runners, or tools.
type Registry struct {
	store   store
	agentID string // optional: scopes all operations to this agent when set
	mainMu  keyedMutex
	// channelMu serializes resolution per chat-channel binding, so two concurrent
	// first messages on one chat cannot each create a session for it.
	channelMu keyedMutex
}

// NewRegistry creates a Registry backed by the given memory.Provider.
// mem must implement memory.SessionManager; if it does not, an error is returned.
// agentID may be empty for a cross-agent registry.
func NewRegistry(mem memory.Provider, agentID string) (*Registry, error) {
	sm, ok := mem.(memory.SessionManager)
	if !ok {
		return nil, fmt.Errorf("memory provider does not implement SessionManager")
	}
	return &Registry{
		store:   newMemoryAdapter(sm),
		agentID: agentID,
	}, nil
}

// NewRegistryWithStore creates a Registry with an explicit store (for testing).
func NewRegistryWithStore(s store, agentID string) *Registry {
	return &Registry{store: s, agentID: agentID}
}

// Ensure finds or creates a session according to the request policy.
//
// - If req.ID is set and the session exists: validates kind and user, returns it.
// - If req.ID is set but missing and AllowExactIDCreate: creates with that ID.
// - If req.ID is empty and CreateIfMissing: generates a new ID and creates.
// - Otherwise: returns ErrNotFound.
func (r *Registry) Ensure(ctx context.Context, req Request) (Info, error) {
	if err := r.validateScope(req.UserID, req.AgentID); err != nil {
		return Info{}, err
	}
	agentID := r.resolveAgentID(req.AgentID)

	if req.ID != "" {
		info, err := r.store.load(ctx, req.ID, req.UserID, agentID)
		if err == nil {
			return r.validateResume(info, req)
		}
		// Session missing: create with exact ID if allowed.
		if req.AllowExactIDCreate && req.CreateIfMissing {
			return r.createWithID(ctx, req.ID, req, agentID)
		}
		return Info{}, fmt.Errorf("%w: %s", ErrNotFound, req.ID)
	}

	if !req.CreateIfMissing {
		return Info{}, ErrNotFound
	}
	return r.createNew(ctx, req, agentID)
}

// Get returns session metadata by ID.
// The session must belong to the given scope (userID + agentID match).
func (r *Registry) Get(ctx context.Context, scope Scope, id string) (Info, error) {
	if err := r.validateScopeObj(scope); err != nil {
		return Info{}, err
	}
	agentID := r.resolveAgentID(scope.AgentID)
	info, err := r.store.load(ctx, id, scope.UserID, agentID)
	if err != nil {
		return Info{}, ErrNotFound
	}
	if !scope.System && info.UserID != scope.UserID {
		return Info{}, ErrForbidden
	}
	return info, nil
}

// List returns sessions matching the scope and options.
func (r *Registry) List(ctx context.Context, scope Scope, opts ListOptions) ([]Info, error) {
	if err := r.validateScopeObj(scope); err != nil {
		return nil, err
	}
	agentID := r.resolveAgentID(scope.AgentID)

	memOpts := memory.ListOptions{
		UserID:          scope.UserID,
		AgentID:         agentID,
		IncludeArchived: opts.IncludeArchived,
		ExcludeInternal: opts.ExcludeInternal,
		ProjectID:       opts.ProjectID,
		Limit:           opts.Limit,
		Offset:          opts.Offset,
	}
	all, err := r.store.list(ctx, scope.UserID, agentID, memOpts)
	if err != nil {
		return nil, err
	}

	if len(opts.Kinds) == 0 {
		return all, nil
	}

	kindSet := make(map[Kind]struct{}, len(opts.Kinds))
	for _, k := range opts.Kinds {
		kindSet[k] = struct{}{}
	}
	out := all[:0]
	for _, info := range all {
		if _, ok := kindSet[Kind(info.Kind)]; ok {
			out = append(out, info)
		}
	}
	return out, nil
}

// ListForAdmin returns an admin's own sessions plus agent-scoped guest sessions.
// The caller must enforce administrator authority and per-row authorization.
func (r *Registry) ListForAdmin(ctx context.Context, userID, agentID string, opts ListOptions) ([]Info, error) {
	if userID == "" {
		return nil, fmt.Errorf("ListForAdmin requires UserID")
	}
	agentID = r.resolveAgentID(agentID)
	if agentID == "" {
		return nil, fmt.Errorf("ListForAdmin requires AgentID")
	}
	lister, ok := r.store.(interface {
		listForAdmin(context.Context, string, string, memory.ListOptions) ([]Info, error)
	})
	if !ok {
		return nil, fmt.Errorf("session store does not support administrative listing")
	}
	all, err := lister.listForAdmin(ctx, userID, agentID, memory.ListOptions{
		UserID:          userID,
		AgentID:         agentID,
		IncludeArchived: opts.IncludeArchived,
		ExcludeInternal: opts.ExcludeInternal,
		ProjectID:       opts.ProjectID,
		Limit:           opts.Limit,
		Offset:          opts.Offset,
	})
	if err != nil || len(opts.Kinds) == 0 {
		return all, err
	}
	kindSet := make(map[Kind]struct{}, len(opts.Kinds))
	for _, kind := range opts.Kinds {
		kindSet[kind] = struct{}{}
	}
	out := all[:0]
	for _, info := range all {
		if _, allowed := kindSet[Kind(info.Kind)]; allowed {
			out = append(out, info)
		}
	}
	return out, nil
}

// Archive marks a session as archived.
func (r *Registry) Archive(ctx context.Context, scope Scope, id string) error {
	info, err := r.Get(ctx, scope, id)
	if err != nil {
		return err
	}
	info.Archived = true
	return r.store.save(ctx, info)
}

// ResolveMain returns the main session for a user+agent pair.
// It first looks for an existing main-kind session, then promotes the most
// recent candidate, and finally creates a new one if CreateIfMissing is set.
func (r *Registry) ResolveMain(ctx context.Context, req MainRequest) (Info, error) {
	if req.UserID == "" || req.AgentID == "" {
		return Info{}, fmt.Errorf("ResolveMain requires UserID and AgentID")
	}
	agentID := r.resolveAgentID(req.AgentID)
	unlock := r.mainMu.lock(agentID + "\x00" + req.UserID)
	defer unlock()

	current, found, err := r.currentMainLocked(ctx, req.UserID, agentID)
	if err != nil {
		return Info{}, err
	}
	if found {
		return current, nil
	}

	info := newMainInfo(agentID, req.UserID)
	if err := r.store.save(ctx, info); err != nil {
		return Info{}, fmt.Errorf("create main session: %w", err)
	}
	return info, nil
}

// RotateMain archives the user's current main session and returns its
// successor, so the next turn starts with an empty context while the old
// session stays searchable. The archive and the create are one store-level
// transaction: the binding is never left without an active main.
//
// When req.ExpectedSessionID is set it must still be the current main;
// otherwise the rotation is stale (another /new already ran) and reports
// ErrStaleRotation without changing anything. Rotating when no main exists is
// just a create.
func (r *Registry) RotateMain(ctx context.Context, req MainRequest) (Info, error) {
	if req.UserID == "" || req.AgentID == "" {
		return Info{}, fmt.Errorf("RotateMain requires UserID and AgentID")
	}
	agentID := r.resolveAgentID(req.AgentID)
	unlock := r.mainMu.lock(agentID + "\x00" + req.UserID)
	defer unlock()

	current, found, err := r.currentMainLocked(ctx, req.UserID, agentID)
	if err != nil {
		return Info{}, err
	}
	successor := newMainInfo(agentID, req.UserID)

	if !found {
		if req.ExpectedSessionID != "" {
			return Info{}, fmt.Errorf("%w: %s is no longer the main session", ErrStaleRotation, req.ExpectedSessionID)
		}
		if err := r.store.save(ctx, successor); err != nil {
			return Info{}, fmt.Errorf("create main session: %w", err)
		}
		return successor, nil
	}
	if req.ExpectedSessionID != "" && req.ExpectedSessionID != current.ID {
		return Info{}, fmt.Errorf("%w: %s is no longer the main session", ErrStaleRotation, req.ExpectedSessionID)
	}
	if err := r.store.rotate(ctx, current.ID, successor); err != nil {
		return Info{}, fmt.Errorf("rotate main session: %w", err)
	}
	return successor, nil
}

// currentMainLocked resolves the session ResolveMain would hand back when one
// already exists: an active main-kind session, or the most recent promotable
// chat candidate. Callers must hold the per-(agent,user) main lock.
func (r *Registry) currentMainLocked(ctx context.Context, userID, agentID string) (Info, bool, error) {
	mains, err := r.store.list(ctx, userID, agentID, memory.ListOptions{
		Kind:            string(KindMain),
		UserID:          userID,
		AgentID:         agentID,
		ProjectIDIsNull: true,
	})
	if err == nil {
		for _, info := range mains {
			return info, true, nil
		}
	}

	// No main session: look for the most recent chat candidate.
	candidates, err := r.store.list(ctx, userID, agentID, memory.ListOptions{
		UserID:  userID,
		AgentID: agentID,
	})
	if err != nil {
		candidates = nil
	}

	var best *Info
	for i := range candidates {
		c := &candidates[i]
		if !isMainCandidate(*c, userID) {
			continue
		}
		if best == nil || c.LastActive.After(best.LastActive) {
			best = c
		}
	}
	if best == nil {
		return Info{}, false, nil
	}

	best.Kind = string(KindMain)
	if err := r.store.save(ctx, *best); err != nil {
		return Info{}, false, fmt.Errorf("promote session to main: %w", err)
	}
	return *best, true, nil
}

// newMainInfo builds a fresh main session. Main sessions use a private user
// channel by convention; isMainCandidate keys promotion off that shape.
func newMainInfo(agentID, userID string) Info {
	now := time.Now().UTC()
	id := uuid.Must(uuid.NewV7()).String()
	channel := agentID + ":user:" + userID + ":private"
	return NewInfo(id, agentID, userID, channel, KindMain, "", now)
}

// ResolveChatChannel returns the chat session a channel chat is currently bound
// to, creating one when the binding has none yet. Unlike Ensure it never uses a
// derived key as the session id, so the binding can rotate onto a successor
// while staying the same chat.
func (r *Registry) ResolveChatChannel(ctx context.Context, req ChannelRequest) (Info, error) {
	req.AgentID = r.resolveAgentID(req.AgentID)
	if err := req.validate(); err != nil {
		return Info{}, err
	}
	unlock := r.channelMu.lock(req.bindingKey())
	defer unlock()

	current, found, err := r.currentChannelLocked(ctx, req)
	if err != nil {
		return Info{}, err
	}
	if found {
		return current, nil
	}

	info := newChannelInfo(req)
	if err := r.store.save(ctx, info); err != nil {
		return Info{}, fmt.Errorf("create chat channel session: %w", err)
	}
	return info, nil
}

// RotateChannel archives the chat channel's current session and returns its
// successor, so the next turn starts with an empty context while the old session
// stays searchable. The archive and the create are one store-level transaction,
// so the binding is never left without an active session.
//
// When req.ExpectedSessionID is set it must still be the binding's current
// session; otherwise the rotation is stale (another `/new` already ran) and
// reports ErrStaleRotation without changing anything.
func (r *Registry) RotateChannel(ctx context.Context, req ChannelRequest) (Info, error) {
	req.AgentID = r.resolveAgentID(req.AgentID)
	if err := req.validate(); err != nil {
		return Info{}, err
	}
	unlock := r.channelMu.lock(req.bindingKey())
	defer unlock()

	current, found, err := r.currentChannelLocked(ctx, req)
	if err != nil {
		return Info{}, err
	}
	successor := newChannelInfo(req)

	if !found {
		if req.ExpectedSessionID != "" {
			return Info{}, fmt.Errorf("%w: %s is no longer the chat session", ErrStaleRotation, req.ExpectedSessionID)
		}
		if err := r.store.save(ctx, successor); err != nil {
			return Info{}, fmt.Errorf("create chat channel session: %w", err)
		}
		return successor, nil
	}
	if req.ExpectedSessionID != "" && req.ExpectedSessionID != current.ID {
		return Info{}, fmt.Errorf("%w: %s is no longer the chat session", ErrStaleRotation, req.ExpectedSessionID)
	}
	if err := r.store.rotate(ctx, current.ID, successor); err != nil {
		return Info{}, fmt.Errorf("rotate chat channel session: %w", err)
	}
	return successor, nil
}

// currentChannelLocked resolves the session currently bound to a chat channel:
// the newest active kind=chat session matching the binding, else the legacy
// key-as-ID session adopted into the binding. Callers must hold the binding lock.
func (r *Registry) currentChannelLocked(ctx context.Context, req ChannelRequest) (Info, bool, error) {
	opts := memory.ListOptions{
		UserID:          req.UserID,
		GuestID:         req.GuestID,
		AgentID:         req.AgentID,
		Kind:            string(KindChat),
		ProjectIDIsNull: true,
	}
	// A group's channel varies with the reply channel a message arrives through,
	// so only a private chat channel binds on it. The group binds on its owner.
	if req.GroupID == "" {
		opts.Channel = string(req.Channel)
	}
	matches, err := r.store.list(ctx, req.UserID, req.AgentID, opts)
	if err != nil {
		return Info{}, false, fmt.Errorf("list chat channel sessions: %w", err)
	}
	// The listing is newest-first, so the most recent match is the live one; older
	// matches are pre-rotation sessions the binding has already left behind.
	if len(matches) > 0 {
		bound, err := r.bindChannelInfo(matches[0], req)
		if err != nil {
			return Info{}, false, err
		}
		// bindChannelInfo backfills a legacy row's binding fields in memory only.
		// Persist them here, exactly as the legacy fallback below does: every later
		// resolve reads the durable row, so an adoption that stays in memory is
		// re-done on every turn and the row's group ownership never becomes real.
		if bound.Channel != matches[0].Channel || bound.GroupID != matches[0].GroupID {
			if err := r.store.save(ctx, bound); err != nil {
				return Info{}, false, fmt.Errorf("persist adopted chat channel binding: %w", err)
			}
		}
		return bound, true, nil
	}

	// Legacy fallback: before the binding existed a chat was pinned to a session
	// whose id was its derived key. Such a row is invisible to the binding query
	// when its channel was never recorded (the column defaults to ''), so adopt it
	// once instead of stranding the user's history behind a new empty session.
	if req.LegacyID == "" {
		return Info{}, false, nil
	}
	// A load miss is the ordinary case (most chats never had a legacy row), and a
	// row this binding must not claim is equivalent to none: either way the caller
	// starts a fresh session rather than failing the turn.
	legacy, loadErr := r.store.load(ctx, req.LegacyID, req.UserID, req.AgentID)
	if loadErr == nil && !legacy.Archived && Kind(legacy.Kind) == KindChat && legacy.ProjectID == "" {
		bound, err := r.bindChannelInfo(legacy, req)
		if err != nil {
			return Info{}, false, err
		}
		if err := r.store.save(ctx, bound); err != nil {
			return Info{}, false, fmt.Errorf("adopt legacy chat channel session: %w", err)
		}
		return bound, true, nil
	}
	return Info{}, false, nil
}

// bindChannelInfo reconciles a candidate against the binding it was resolved
// for, backfilling the fields a legacy row may lack. It fails closed on a
// mismatch: the durable group owner is an ownership claim, never a rebind.
func (r *Registry) bindChannelInfo(info Info, req ChannelRequest) (Info, error) {
	if info.UserID != req.UserID || info.AgentID != req.AgentID || info.GuestID != req.GuestID {
		return Info{}, fmt.Errorf("%w: %s", ErrForbidden, info.ID)
	}
	if info.Channel == "" && !req.Channel.isZero() {
		info.Channel = string(req.Channel)
	}
	if req.GroupID != "" {
		switch info.GroupID {
		case req.GroupID:
		case "":
			// Legacy row with a NULL group_id whose owner is this group: reattach.
			info.GroupID = req.GroupID
		default:
			return Info{}, fmt.Errorf("%w: session %s belongs to group %q, not %q", ErrForbidden, info.ID, info.GroupID, req.GroupID)
		}
	} else if info.GroupID != "" {
		return Info{}, fmt.Errorf("%w: session %s is owned by group %q", ErrForbidden, info.ID, info.GroupID)
	}
	if err := info.Validate(); err != nil {
		return Info{}, err
	}
	return info, nil
}

// newChannelInfo builds a fresh session carrying a chat channel's binding.
func newChannelInfo(req ChannelRequest) Info {
	now := time.Now().UTC()
	id := uuid.Must(uuid.NewV7()).String()
	info := NewInfo(id, req.AgentID, req.UserID, string(req.Channel), KindChat, "", now)
	info.GroupID = req.GroupID
	info.GuestID = req.GuestID
	return info
}

// ListForReview returns sessions that are candidates for reflect review.
func (r *Registry) ListForReview(ctx context.Context, req ReviewRequest) ([]Info, error) {
	agentID := r.resolveAgentID(req.AgentID)
	if agentID == "" {
		return nil, fmt.Errorf("ListForReview requires AgentID")
	}

	policy := req.Policy
	if policy.IsZero() {
		policy = DefaultReviewPolicy()
	}

	// Archived sessions stay candidates: rotation (/new) archives a session the
	// instant the user starts a fresh one, and its final messages still have to
	// reach reflect. The caller drops them once their watermarks catch up.
	all, err := r.store.listForReview(ctx, agentID, memory.ListOptions{
		AgentID:         agentID,
		IncludeArchived: true,
	})
	if err != nil {
		return nil, err
	}

	out := all[:0]
	for _, info := range all {
		if !policy.Includes(Kind(info.Kind)) {
			continue
		}
		out = append(out, info)
	}
	return out, nil
}

// MemoryScope converts a validated session Info into a memory.Session scope.
// This is the ONLY authorised way to produce memory.Session for production agent
// sessions; it delegates to the canonical Info.MemoryScope conversion. Private
// read, write, and compaction resolve one partition; group read and write share
// the durable canonical group scope (group compaction is unsupported, so
// MemoryScope makes no claim about it). It fails closed on an invalid Info.
func (r *Registry) MemoryScope(info Info) (memory.Session, error) {
	return info.MemoryScope()
}

// --- internal helpers -------------------------------------------------------

func (r *Registry) resolveAgentID(agentID string) string {
	if agentID != "" {
		return agentID
	}
	return r.agentID
}

func (r *Registry) validateScope(userID, agentID string) error {
	if userID == "" {
		return fmt.Errorf("UserID is required")
	}
	if r.resolveAgentID(agentID) == "" {
		return fmt.Errorf("AgentID is required")
	}
	return nil
}

func (r *Registry) validateScopeObj(scope Scope) error {
	if !scope.System && scope.UserID == "" {
		return fmt.Errorf("UserID is required for non-system scope")
	}
	if r.resolveAgentID(scope.AgentID) == "" {
		return fmt.Errorf("AgentID is required")
	}
	return nil
}

func (r *Registry) validateResume(info Info, req Request) (Info, error) {
	if req.UserID != "" && info.UserID != req.UserID {
		return Info{}, fmt.Errorf("%w: %s", ErrForbidden, info.ID)
	}
	if agentID := r.resolveAgentID(req.AgentID); agentID != "" && info.AgentID != agentID {
		return Info{}, fmt.Errorf("%w: %s", ErrForbidden, info.ID)
	}
	if info.Archived {
		return Info{}, fmt.Errorf("%w: %s", ErrArchived, info.ID)
	}
	if req.RequireKind != "" && Kind(info.Kind) != req.RequireKind {
		return Info{}, fmt.Errorf("%w: got %q, want %q", ErrWrongKind, info.Kind, req.RequireKind)
	}
	if !req.Channel.isZero() && info.Channel == "" {
		info.Channel = string(req.Channel)
	}
	// Reconcile the requested group identity with the durable one.
	if req.GroupID != "" {
		switch {
		case info.GroupID == req.GroupID:
			// Durable group_id already matches; nothing to do.
		case info.GroupID != "":
			// A different durable group owns this session: reject rather than rebind.
			return Info{}, fmt.Errorf("%w: session %s belongs to group %q, not %q", ErrForbidden, info.ID, info.GroupID, req.GroupID)
		case info.UserID == req.GroupID:
			// Legacy row with a NULL group_id whose owner is this group: reattach.
			info.GroupID = req.GroupID
		default:
			return Info{}, fmt.Errorf("%w: group %q does not own session %s", ErrForbidden, req.GroupID, info.ID)
		}
	}
	// Fail closed: never hand back a resumed Info that violates the invariant.
	if err := info.Validate(); err != nil {
		return Info{}, err
	}
	return info, nil
}

func (r *Registry) createNew(ctx context.Context, req Request, agentID string) (Info, error) {
	now := time.Now().UTC()
	id := uuid.Must(uuid.NewV7()).String()
	channel := defaultChannel(req.Channel, req.Kind)
	info := NewInfo(id, agentID, req.UserID, channel, req.Kind, req.ProjectID, now)
	info.GroupID = req.GroupID
	info.Title = req.Title
	if req.Kind == "" {
		info.Kind = string(KindChat)
	}
	if err := r.store.save(ctx, info); err != nil {
		return Info{}, fmt.Errorf("create session: %w", err)
	}
	return info, nil
}

func (r *Registry) createWithID(ctx context.Context, id string, req Request, agentID string) (Info, error) {
	now := time.Now().UTC()
	channel := defaultChannel(req.Channel, req.Kind)
	info := NewInfo(id, agentID, req.UserID, channel, req.Kind, req.ProjectID, now)
	info.GroupID = req.GroupID
	info.Title = req.Title
	if req.Kind == "" {
		info.Kind = string(KindChat)
	}
	if err := r.store.save(ctx, info); err != nil {
		return Info{}, fmt.Errorf("create session with ID %q: %w", id, err)
	}
	return info, nil
}

func defaultChannel(ch Channel, kind Kind) string {
	if ch != "" {
		return string(ch)
	}
	switch kind {
	case KindDelegate:
		return string(ChannelDelegate)
	case KindTask:
		return string(ChannelTask)
	case KindScheduler:
		return string(ChannelScheduler)
	default:
		return string(ChannelWeb)
	}
}

func (ch Channel) isZero() bool {
	return strings.TrimSpace(string(ch)) == ""
}

type keyedMutex struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func (m *keyedMutex) lock(key string) func() {
	m.mu.Lock()
	if m.locks == nil {
		m.locks = make(map[string]*sync.Mutex)
	}
	lock := m.locks[key]
	if lock == nil {
		lock = &sync.Mutex{}
		m.locks[key] = lock
	}
	m.mu.Unlock()

	lock.Lock()
	return lock.Unlock
}
