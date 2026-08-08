// Package session owns agent-session lifecycle.
//
// It is the sole authority over session creation, resumption, kind/channel
// policy, main-session resolution, review candidate selection, and the
// conversion from a validated session record to a memory operation scope.
//
// Info is a session-domain type, not an alias to memory.SessionInfo. Every use
// boundary fails closed: the persistence mapping (Record / InfoFromRecord) and
// the memory-scope conversion (MemoryScope) validate the session invariant and
// return an error rather than emit an unchecked value. The memory scope is
// produced only by MemoryScope; production code must not hand-build
// memory.Session values.
package session

import (
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/memory"
)

// Kind is the typed session kind.
type Kind string

const (
	KindMain      Kind = "main"
	KindChat      Kind = "chat"
	KindDelegate  Kind = "delegate"
	KindTask      Kind = "task"
	KindScheduler Kind = "scheduler"
)

// Channel is the typed originating channel.
type Channel string

const (
	ChannelWeb       Channel = "web"
	ChannelCLI       Channel = "cli"
	ChannelTelegram  Channel = "telegram"
	ChannelDelegate  Channel = "delegate"
	ChannelTask      Channel = "task"
	ChannelScheduler Channel = "scheduler"
	ChannelWebhook   Channel = "webhook"
)

// Info holds validated metadata about an agent session. It is a session-domain
// value distinct from the memory-layer persistence type memory.SessionInfo;
// callers cross the persistence boundary through Record and InfoFromRecord.
//
// GroupID identifies the group a group session belongs to. It is now durable
// (ctx_conversation.group_id) and load/save round-trip it. The durable invariant
// is that a group session is owned by the group itself: UserID equals GroupID,
// and GroupID is the canonical ctx_group_state UUID. That equality is what makes
// group read, write, and compaction resolve one memory partition; it is enforced
// by Validate at every use boundary rather than left as an undeclared convention.
type Info struct {
	ID                  string
	AgentID             string
	UserID              string
	GroupID             string
	GuestID             string
	Channel             string
	Kind                string
	ProjectID           string
	Title               string
	CreatedAt           time.Time
	LastActive          time.Time
	LastTurnStartedAt   time.Time
	LastTurnCompletedAt time.Time
	LastTurnResult      memory.SessionTurnResult
	LastViewedAt        time.Time
	Archived            bool
	// LatestSeq is populated by review listings and is not session metadata.
	LatestSeq int64
}

// NewInfo constructs a fresh Info with the required fields set.
func NewInfo(id, agentID, userID, channel string, kind Kind, projectID string, now time.Time) Info {
	return Info{
		ID:         id,
		AgentID:    agentID,
		UserID:     userID,
		Channel:    channel,
		Kind:       string(kind),
		ProjectID:  projectID,
		CreatedAt:  now,
		LastActive: now,
	}
}

// Validate enforces the durable session-scope invariant. Every session needs an
// ID, an agent, and a user owner. A group session is owned by its group: UserID
// must equal GroupID and GroupID must be a canonical ctx_group_state UUID. This
// is the single fail-closed gate the persistence and memory-scope boundaries run.
func (i Info) Validate() error {
	if i.ID == "" {
		return fmt.Errorf("session info: missing ID")
	}
	if i.AgentID == "" {
		return fmt.Errorf("session info %q: missing AgentID", i.ID)
	}
	if i.UserID == "" {
		return fmt.Errorf("session info %q: missing UserID", i.ID)
	}
	if i.GroupID != "" {
		if i.GuestID != "" {
			return fmt.Errorf("session info %q: group session cannot have GuestID", i.ID)
		}
		if i.UserID != i.GroupID {
			return fmt.Errorf("session info %q: group session UserID %q must equal GroupID %q", i.ID, i.UserID, i.GroupID)
		}
		// Require the exact canonical lowercase-hyphenated UUID form. uuid.Parse
		// also accepts braces/urn/uppercase/hyphenless variants, but the durable
		// group id is a canonical ctx_group_state UUID, so anything that does not
		// round-trip through uuid.String() is rejected.
		parsed, err := uuid.Parse(i.GroupID)
		if err != nil {
			return fmt.Errorf("session info %q: GroupID %q is not a valid group id: %w", i.ID, i.GroupID, err)
		}
		if parsed.String() != i.GroupID {
			return fmt.Errorf("session info %q: GroupID %q is not in canonical UUID form", i.ID, i.GroupID)
		}
	}
	if i.GuestID != "" {
		if i.UserID != i.GuestID {
			return fmt.Errorf("session info %q: guest session UserID %q must equal GuestID %q", i.ID, i.UserID, i.GuestID)
		}
		parsed, err := uuid.Parse(i.GuestID)
		if err != nil || parsed.String() != i.GuestID {
			return fmt.Errorf("session info %q: GuestID %q is not a canonical UUID", i.ID, i.GuestID)
		}
		if i.GroupID != "" || i.ProjectID != "" || Kind(i.Kind) != KindChat {
			return fmt.Errorf("session info %q: guest session must be chat kind without group or project", i.ID)
		}
	}
	return nil
}

// MemoryScope is the single canonical conversion from a validated session Info
// to a memory.Session operation scope. It fails closed: an Info that violates the
// session invariant yields an error, never a half-formed scope.
//
// For a private session, read (Assemble), write (Append/Bootstrap), and
// compaction all derive their scope here and land on the same partition. A group
// session shares one durable canonical scope for read and write (partition keyed
// on session/user/agent, with GroupID selecting the group assembly/isolation
// path); group compaction is a separate matter and is unsupported (rejected by
// agent.Service.CompactSession), because group history is assembled from the
// group event log rather than the LCM conversation.
func (i Info) MemoryScope() (memory.Session, error) {
	if err := i.Validate(); err != nil {
		return memory.Session{}, err
	}
	return memory.Session{
		ID:      i.ID,
		AgentID: i.AgentID,
		UserID:  i.UserID,
		Channel: i.Channel,
		GroupID: i.GroupID,
		GuestID: i.GuestID,
	}, nil
}

// Record maps a validated session-domain Info onto the memory-layer persistence
// type. It is the domain→persistence half of the session store boundary and
// fails closed on an invalid Info.
func (i Info) Record() (memory.SessionInfo, error) {
	if err := i.Validate(); err != nil {
		return memory.SessionInfo{}, err
	}
	return memory.SessionInfo{
		ID:                  i.ID,
		AgentID:             i.AgentID,
		UserID:              i.UserID,
		GroupID:             i.GroupID,
		GuestID:             i.GuestID,
		Channel:             i.Channel,
		Kind:                i.Kind,
		ProjectID:           i.ProjectID,
		Title:               i.Title,
		CreatedAt:           i.CreatedAt,
		LastActive:          i.LastActive,
		LastTurnStartedAt:   i.LastTurnStartedAt,
		LastTurnCompletedAt: i.LastTurnCompletedAt,
		LastTurnResult:      i.LastTurnResult,
		LastViewedAt:        i.LastViewedAt,
		Archived:            i.Archived,
		LatestSeq:           i.LatestSeq,
	}, nil
}

// InfoFromRecord maps a memory-layer persistence record back to a session-domain
// Info. It is the persistence→domain half of the session store boundary and the
// only sanctioned way to turn an unvalidated memory.SessionInfo into an Info; it
// validates the resulting Info and fails closed on a record that violates the
// session invariant.
func InfoFromRecord(r memory.SessionInfo) (Info, error) {
	info := Info{
		ID:                  r.ID,
		AgentID:             r.AgentID,
		UserID:              r.UserID,
		GroupID:             r.GroupID,
		GuestID:             r.GuestID,
		Channel:             r.Channel,
		Kind:                r.Kind,
		ProjectID:           r.ProjectID,
		Title:               r.Title,
		CreatedAt:           r.CreatedAt,
		LastActive:          r.LastActive,
		LastTurnStartedAt:   r.LastTurnStartedAt,
		LastTurnCompletedAt: r.LastTurnCompletedAt,
		LastTurnResult:      r.LastTurnResult,
		LastViewedAt:        r.LastViewedAt,
		Archived:            r.Archived,
		LatestSeq:           r.LatestSeq,
	}
	if err := info.Validate(); err != nil {
		return Info{}, err
	}
	return info, nil
}

// infosFromRecords maps a slice of persistence records to validated domain Info
// values, failing closed if any record violates the session invariant.
func infosFromRecords(rs []memory.SessionInfo) ([]Info, error) {
	out := make([]Info, len(rs))
	for i, r := range rs {
		info, err := InfoFromRecord(r)
		if err != nil {
			return nil, fmt.Errorf("session record %d: %w", i, err)
		}
		out[i] = info
	}
	return out, nil
}

// infosFromReviewRecords maps review-listing records to validated Info values.
// Unlike infosFromRecords it skips a legacy ownerless row (empty UserID) instead
// of failing: such rows were historically excluded from review, so one must not
// fail an agent's entire review list forever. Any non-empty malformed record
// (group mismatch, non-canonical id) still fails closed.
func infosFromReviewRecords(rs []memory.SessionInfo) ([]Info, error) {
	out := make([]Info, 0, len(rs))
	for i, r := range rs {
		if r.UserID == "" || r.GuestID != "" {
			continue
		}
		info, err := InfoFromRecord(r)
		if err != nil {
			return nil, fmt.Errorf("session record %d: %w", i, err)
		}
		out = append(out, info)
	}
	return out, nil
}
