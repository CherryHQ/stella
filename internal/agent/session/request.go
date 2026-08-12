package session

import "fmt"

// Request describes what session to find or create.
type Request struct {
	// ID is the exact session ID to resume. If empty, a new ID is generated.
	ID string
	// UserID and AgentID are required for user-scoped operations.
	UserID  string
	AgentID string
	// GroupID marks this as a group session owned by the group. When set it is
	// the canonical ctx_group_state UUID and must equal UserID; it is persisted
	// so the group identity survives reload. Empty for private sessions.
	GroupID string
	// ProjectID scopes the session to a project when non-empty.
	ProjectID string
	// Kind is the session kind to use when creating a new session.
	// If the session already exists, it must match RequireKind when set.
	Kind Kind
	// Channel is the originating channel (e.g. ChannelWeb, ChannelDelegate).
	Channel Channel
	// Title is optional; pool_chat auto-derives it from the first message.
	Title string
	// CreateIfMissing creates a new session if none matches.
	CreateIfMissing bool
	// AllowExactIDCreate allows creating a new session with the explicit ID
	// when that ID does not yet exist. Required for delegate session persistence.
	AllowExactIDCreate bool
	// RequireKind enforces that a resumed session must have this kind.
	// Empty means any kind is acceptable.
	RequireKind Kind
}

// Scope identifies a caller's access scope for registry queries.
type Scope struct {
	UserID  string
	AgentID string
	// System marks this as a background-only scope (e.g. reflect).
	// System scopes bypass user requirement but still require AgentID.
	System bool
}

// ListOptions controls session listing.
type ListOptions struct {
	// Kinds filters by session kind; empty = all kinds.
	Kinds []Kind
	// IncludeArchived includes archived sessions.
	IncludeArchived bool
	// ExcludeInternal omits task/delegate worker sessions from human lists.
	ExcludeInternal bool
	// ProjectID filters to one project when non-empty.
	ProjectID string
	Limit     int
	Offset    int
}

// MainRequest describes a main-session resolution or rotation request.
type MainRequest struct {
	UserID  string
	AgentID string
	// ExpectedSessionID makes a rotation a compare-and-rotate: the current main
	// must still be this session, otherwise RotateMain reports ErrStaleRotation
	// and changes nothing. Empty means "rotate whatever is current"; it is
	// ignored by ResolveMain.
	ExpectedSessionID string
}

// ChannelRequest describes a durable chat-channel session binding: the identity
// a channel chat resolves against instead of pinning itself to one session id.
//
// A group binds on its owning group (UserID == GroupID) plus agent and kind=chat,
// because a group's channel string varies with the platform reply channel it
// arrives through. A private chat channel binds on user + agent + kind=chat +
// Channel, which is the channel adapter's stable session key.
type ChannelRequest struct {
	UserID  string
	AgentID string
	// GuestID marks a persistent guest binding and must equal UserID.
	GuestID string
	// GroupID marks a group binding. When set it must equal UserID.
	GroupID string
	// Channel is the durable channel value the binding carries. It is part of the
	// binding predicate for private chat channels and informational for groups.
	Channel Channel
	// LegacyID is the pre-binding key-as-ID session for this chat. On a binding
	// miss it is resolved once and, when it is still a valid active session for
	// this binding, adopted (binding fields persisted) instead of superseded by a
	// new empty session. Empty disables the fallback.
	LegacyID string
	// ExpectedSessionID makes a rotation a compare-and-rotate: the binding's
	// current session must still be this one, otherwise RotateChannel reports
	// ErrStaleRotation and changes nothing. Ignored by ResolveChatChannel.
	ExpectedSessionID string
}

func (r ChannelRequest) validate() error {
	if r.UserID == "" || r.AgentID == "" {
		return fmt.Errorf("chat channel binding requires UserID and AgentID")
	}
	if r.GroupID != "" && r.GroupID != r.UserID {
		return fmt.Errorf("chat channel binding: group session UserID %q must equal GroupID %q", r.UserID, r.GroupID)
	}
	if r.GuestID != "" && (r.GuestID != r.UserID || r.GroupID != "") {
		return fmt.Errorf("chat channel binding: guest session requires UserID == GuestID and no group")
	}
	if r.GroupID == "" && r.Channel.isZero() {
		return fmt.Errorf("chat channel binding requires Channel")
	}
	return nil
}

// bindingKey identifies the binding for the in-process resolve lock. Two
// concurrent first messages on one chat must not each create a session.
//
// The key has to be exactly the predicate currentChannelLocked resolves on, or
// the lock guards the wrong thing. A group resolves on (agent, group) alone —
// its channel varies with the reply channel a message arrives through — so
// keying the lock on the channel too would let the Web send and the platform
// ingest of one group's first message take different locks and each create an
// active session. Private chats do bind on their channel, so it stays in theirs.
func (r ChannelRequest) bindingKey() string {
	channel := string(r.Channel)
	if r.GroupID != "" {
		channel = ""
	}
	return r.AgentID + "\x00" + r.UserID + "\x00" + r.GroupID + "\x00" + r.GuestID + "\x00" + channel
}

// ReviewRequest describes which sessions are candidates for reflect review.
type ReviewRequest struct {
	AgentID string
	// Policy controls inclusion/exclusion rules.
	Policy ReviewPolicy
}
