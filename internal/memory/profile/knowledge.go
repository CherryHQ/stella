package profile

import (
	"context"
	"errors"
	"time"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/memorywrite"
)

// KnowledgeManager is the narrow persistence port for the user-facing knowledge
// lifecycle and the keyset changelog projection. *memorywrite.ManagementService
// satisfies it. Profile is the only caller: every method here is reached through
// a Service method that has already applied the Agent gate and derived the owner
// tuple from the trusted Authority, so the manager never sees a caller-supplied
// owner id.
type KnowledgeManager interface {
	ListKnowledge(ctx context.Context, in memorywrite.KnowledgeListQuery) (memorywrite.KnowledgePage, error)
	CreateKnowledge(ctx context.Context, in memorywrite.KnowledgeCreateInput) (memory.Fact, error)
	ReplaceKnowledge(ctx context.Context, in memorywrite.KnowledgeReplaceInput) (memory.Fact, error)
	DeprecateKnowledge(ctx context.Context, in memorywrite.KnowledgeDeprecateInput) (memory.Fact, error)
	RestoreKnowledge(ctx context.Context, in memorywrite.KnowledgeRestoreInput) (memorywrite.KnowledgeRestoreResult, error)
	ReadChangelogPage(ctx context.Context, userID string, agentID string, scope string, cursor *memory.ChangelogCursor, limit int) ([]memory.ChangeEntry, error)
}

// Knowledge lifecycle errors, owned by this boundary so the transport maps them
// without importing the persistence layer. They wrap the memorywrite sentinels so
// an errors.Is against either matches.
var (
	// ErrKnowledgeUnavailable reports the knowledge/changelog-page backend is not
	// wired (Provider without the keyset changelog capability) — a 503.
	ErrKnowledgeUnavailable = errors.New("knowledge management not configured")
	// ErrKnowledgeNotFound reports a knowledge item the caller's agent memory does
	// not contain (404).
	ErrKnowledgeNotFound = errors.New("knowledge not found")
	// ErrKnowledgeNotRestorable reports a lifecycle transition that does not apply
	// to the item's current state (404 on edit/delete, 409 on restore).
	ErrKnowledgeNotRestorable = errors.New("knowledge not restorable")
	// ErrKnowledgeDuplicateContent reports a restore blocked by an active item with
	// identical content (409).
	ErrKnowledgeDuplicateContent = errors.New("active knowledge already has this content")
	// ErrKnowledgeRestoreExpired reports a restore past the retention window (410).
	ErrKnowledgeRestoreExpired = errors.New("knowledge restore window expired")
)

// KnowledgeState selects the active or removed lifecycle slice.
type KnowledgeState string

const (
	KnowledgeStateActive  KnowledgeState = "active"
	KnowledgeStateRemoved KnowledgeState = "removed"
)

// KnowledgeCursor is the transport-neutral keyset cursor for a knowledge page.
type KnowledgeCursor struct {
	Timestamp time.Time
	ID        string
}

// KnowledgeItem is one knowledge row projected for presentation, with its
// removal metadata for the removed slice.
type KnowledgeItem struct {
	Fact            memory.Fact
	RemovalSource   string
	DeprecatedAt    *time.Time
	RestoreDeadline *time.Time
	IsRestorable    bool
}

// KnowledgePage is one deterministic knowledge page with its keyset cursor.
type KnowledgePage struct {
	Items      []KnowledgeItem
	Total      int64
	HasMore    bool
	NextCursor *KnowledgeCursor
}

// ListKnowledge returns one active or removed knowledge page for the caller's
// agent memory. Gated on agent read access; the owner tuple is the Authority's.
func (s *Service) ListKnowledge(ctx context.Context, authority authz.Authority, agentID string, state KnowledgeState, limit int, cursor *KnowledgeCursor) (KnowledgePage, error) {
	if err := s.gate(ctx, authority, agentID); err != nil {
		return KnowledgePage{}, err
	}
	if s.knowledge == nil {
		return KnowledgePage{}, ErrKnowledgeUnavailable
	}
	page, err := s.knowledge.ListKnowledge(ctx, memorywrite.KnowledgeListQuery{
		UserID:  string(authority.UserID()),
		AgentID: agentID,
		State:   memorywrite.KnowledgeState(state),
		Limit:   int32(limit),
		Now:     time.Now().UTC(),
		Cursor:  knowledgeCursorToWrite(cursor),
	})
	if err != nil {
		return KnowledgePage{}, translateKnowledgeError(err)
	}
	return knowledgePageFromWrite(page), nil
}

// CreateKnowledge adds one active knowledge item to the caller's agent memory.
func (s *Service) CreateKnowledge(ctx context.Context, authority authz.Authority, agentID, content string) (memory.Fact, error) {
	if err := s.gate(ctx, authority, agentID); err != nil {
		return memory.Fact{}, err
	}
	if s.knowledge == nil {
		return memory.Fact{}, ErrKnowledgeUnavailable
	}
	fact, err := s.knowledge.CreateKnowledge(ctx, memorywrite.KnowledgeCreateInput{
		UserID: string(authority.UserID()), AgentID: agentID, Content: content,
	})
	return fact, translateKnowledgeError(err)
}

// ReplaceKnowledge edits an active knowledge item in place (deprecate + create).
func (s *Service) ReplaceKnowledge(ctx context.Context, authority authz.Authority, agentID, factID, content string) (memory.Fact, error) {
	if err := s.gate(ctx, authority, agentID); err != nil {
		return memory.Fact{}, err
	}
	if s.knowledge == nil {
		return memory.Fact{}, ErrKnowledgeUnavailable
	}
	fact, err := s.knowledge.ReplaceKnowledge(ctx, memorywrite.KnowledgeReplaceInput{
		FactID: factID, UserID: string(authority.UserID()), AgentID: agentID, Content: content,
	})
	return fact, translateKnowledgeError(err)
}

// DeprecateKnowledge removes (soft-deletes) an active knowledge item. The
// deprecating actor is the authenticated caller, never a request field.
func (s *Service) DeprecateKnowledge(ctx context.Context, authority authz.Authority, agentID, factID string) error {
	if err := s.gate(ctx, authority, agentID); err != nil {
		return err
	}
	if s.knowledge == nil {
		return ErrKnowledgeUnavailable
	}
	userID := string(authority.UserID())
	_, err := s.knowledge.DeprecateKnowledge(ctx, memorywrite.KnowledgeDeprecateInput{
		FactID: factID, UserID: userID, AgentID: agentID, DeprecatedBy: userID,
	})
	return translateKnowledgeError(err)
}

// RestoreKnowledge restores a removed knowledge item within the retention
// window. The restoring actor is the authenticated caller, never a request field.
func (s *Service) RestoreKnowledge(ctx context.Context, authority authz.Authority, agentID, factID string) (memory.Fact, error) {
	if err := s.gate(ctx, authority, agentID); err != nil {
		return memory.Fact{}, err
	}
	if s.knowledge == nil {
		return memory.Fact{}, ErrKnowledgeUnavailable
	}
	userID := string(authority.UserID())
	result, err := s.knowledge.RestoreKnowledge(ctx, memorywrite.KnowledgeRestoreInput{
		FactID: factID, UserID: userID, AgentID: agentID, RestoredBy: userID, Now: time.Now().UTC(),
	})
	if err != nil {
		return memory.Fact{}, translateKnowledgeError(err)
	}
	return result.Fact, nil
}

// ChangelogPage returns one keyset-paginated logical page of change history for a
// single scope of the caller's agent memory. Gated on agent read access; the
// owner tuple is the Authority's. The transport merges scopes and derives the
// next page token.
func (s *Service) ChangelogPage(ctx context.Context, authority authz.Authority, agentID, scope string, cursor *memory.ChangelogCursor, limit int) ([]memory.ChangeEntry, error) {
	if err := s.gate(ctx, authority, agentID); err != nil {
		return nil, err
	}
	if s.knowledge == nil {
		return nil, ErrKnowledgeUnavailable
	}
	entries, err := s.knowledge.ReadChangelogPage(ctx, string(authority.UserID()), agentID, scope, cursor, limit)
	if err != nil {
		return nil, err
	}
	return entries, nil
}

// translateKnowledgeError maps the persistence layer's fact sentinels onto this
// boundary's knowledge sentinels so the transport never matches a memorywrite
// error. Agent-gate and other errors pass through unchanged.
func translateKnowledgeError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, memorywrite.ErrFactNotFound):
		return ErrKnowledgeNotFound
	case errors.Is(err, memorywrite.ErrFactDuplicateContent):
		return ErrKnowledgeDuplicateContent
	case errors.Is(err, memorywrite.ErrFactRestoreExpired):
		return ErrKnowledgeRestoreExpired
	case errors.Is(err, memorywrite.ErrFactNotRestorable):
		return ErrKnowledgeNotRestorable
	default:
		return err
	}
}

func knowledgeCursorToWrite(cursor *KnowledgeCursor) *memorywrite.KnowledgeCursor {
	if cursor == nil {
		return nil
	}
	return &memorywrite.KnowledgeCursor{Timestamp: cursor.Timestamp, ID: cursor.ID}
}

func knowledgePageFromWrite(page memorywrite.KnowledgePage) KnowledgePage {
	items := make([]KnowledgeItem, len(page.Items))
	for i, item := range page.Items {
		items[i] = KnowledgeItem{
			Fact:            item.Fact,
			RemovalSource:   string(item.RemovalSource),
			DeprecatedAt:    item.DeprecatedAt,
			RestoreDeadline: item.RestoreDeadline,
			IsRestorable:    item.IsRestorable,
		}
	}
	out := KnowledgePage{Items: items, Total: page.Total, HasMore: page.HasMore}
	if page.NextCursor != nil {
		out.NextCursor = &KnowledgeCursor{Timestamp: page.NextCursor.Timestamp, ID: page.NextCursor.ID}
	}
	return out
}
