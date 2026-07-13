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

// Registry is the sole owner of agent-session lifecycle.
// It creates, resumes, lists, and archives sessions; it also converts validated
// session records into memory operation scopes.
//
// Registry has no knowledge of LLM execution, runners, or tools.
type Registry struct {
	store   store
	agentID string // optional: scopes all operations to this agent when set
	mainMu  keyedMutex
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

	// Look for an existing main-kind session.
	mains, err := r.store.list(ctx, req.UserID, agentID, memory.ListOptions{
		Kind:            string(KindMain),
		UserID:          req.UserID,
		AgentID:         agentID,
		ProjectIDIsNull: true,
	})
	if err == nil {
		for _, info := range mains {
			return info, nil
		}
	}

	// No main session: look for the most recent chat candidate.
	candidates, err := r.store.list(ctx, req.UserID, agentID, memory.ListOptions{
		UserID:  req.UserID,
		AgentID: agentID,
	})
	if err != nil {
		candidates = nil
	}

	var best *Info
	for i := range candidates {
		c := &candidates[i]
		if !isMainCandidate(*c, req.UserID) {
			continue
		}
		if best == nil || c.LastActive.After(best.LastActive) {
			best = c
		}
	}

	if best != nil {
		best.Kind = string(KindMain)
		if err := r.store.save(ctx, *best); err != nil {
			return Info{}, fmt.Errorf("promote session to main: %w", err)
		}
		return *best, nil
	}

	// Create a fresh main session.
	now := time.Now().UTC()
	id := uuid.Must(uuid.NewV7()).String()
	// Main sessions use a private user channel by convention.
	channel := agentID + ":user:" + req.UserID + ":private"
	info := NewInfo(id, agentID, req.UserID, channel, KindMain, "", now)
	if err := r.store.save(ctx, info); err != nil {
		return Info{}, fmt.Errorf("create main session: %w", err)
	}
	return info, nil
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

	all, err := r.store.listForReview(ctx, agentID, memory.ListOptions{
		AgentID: agentID,
	})
	if err != nil {
		return nil, err
	}

	out := all[:0]
	for _, info := range all {
		if info.Archived {
			continue
		}
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
