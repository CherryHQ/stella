package session

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
	Limit           int
	Offset          int
}

// MainRequest describes a main-session resolution request.
type MainRequest struct {
	UserID  string
	AgentID string
}

// ReviewRequest describes which sessions are candidates for reflect review.
type ReviewRequest struct {
	AgentID string
	// Policy controls inclusion/exclusion rules.
	Policy ReviewPolicy
}
