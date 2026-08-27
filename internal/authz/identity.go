package authz

import (
	"context"
	"errors"
)

type contextKey string

const (
	userIDKey    contextKey = "memory_user_id"
	agentIDKey   contextKey = "memory_agent_id"
	groupIDKey   contextKey = "memory_group_id"
	guestIDKey   contextKey = "memory_guest_id"
	authorityKey contextKey = "stella_turn_authority"
)

type authorityContextValue struct {
	value Authority
	set   bool
}

var (
	ErrNotFound        = errors.New("not found")
	ErrForbidden       = errors.New("permission denied")
	ErrUnauthenticated = errors.New("authentication required - ask the user to run this from a signed-in one-on-one session")
)

// WithUserID attaches a user ID to the context.
func WithUserID(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, userIDKey, userID)
}

// UserIDFromContext extracts the user ID from context.
func UserIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(userIDKey).(string)
	return id
}

// WithAgentID attaches an agent ID to the context.
func WithAgentID(ctx context.Context, agentID string) context.Context {
	return context.WithValue(ctx, agentIDKey, agentID)
}

// AgentIDFromContext extracts the agent ID from context.
func AgentIDFromContext(ctx context.Context) string {
	s, _ := ctx.Value(agentIDKey).(string)
	return s
}

// WithGroupID attaches a group ID to the context. Group turns carry the group
// (not a user) so a trusted adapter can reconstruct a confined GroupAgentActor
// without ever minting a user identity for the group (D9 isolation).
func WithGroupID(ctx context.Context, groupID string) context.Context {
	return context.WithValue(ctx, groupIDKey, groupID)
}

// GroupIDFromContext extracts the group ID from context.
func GroupIDFromContext(ctx context.Context) string {
	s, _ := ctx.Value(groupIDKey).(string)
	return s
}

func WithGuestID(ctx context.Context, guestID string) context.Context {
	return context.WithValue(ctx, guestIDKey, guestID)
}

func GuestIDFromContext(ctx context.Context) string {
	s, _ := ctx.Value(guestIDKey).(string)
	return s
}

// WithAuthority carries one immutable direct-human turn capability. It is
// intentionally separate from the legacy identity fields: cached runners never
// own this value, and nested/durable paths do not set it.
func WithAuthority(ctx context.Context, authority Authority) context.Context {
	return context.WithValue(ctx, authorityKey, authorityContextValue{value: authority, set: true})
}

// ClearAuthority masks any carrier inherited from a caller context. Runtime
// invokes this at the start of every turn before deciding whether this turn may
// receive a direct-human Authority.
func ClearAuthority(ctx context.Context) context.Context {
	return context.WithValue(ctx, authorityKey, authorityContextValue{set: false})
}

func AuthorityFromContext(ctx context.Context) (Authority, bool) {
	value, ok := ctx.Value(authorityKey).(authorityContextValue)
	if !ok || !value.set || !value.value.Valid() {
		return Authority{}, false
	}
	return value.value, true
}

// Identity is the non-spoofable runtime identity carried by context or an HTTP
// adapter. AgentScoped means access must stay inside AgentID's resource boundary.
type Identity struct {
	UserID      string
	AgentID     string
	AgentScoped bool
}

// FromContext extracts the runtime identity for built-in tools. Group and
// unauthenticated sessions have no user id, so identity-scoped tools must refuse
// them. An agent session is always confined to its own agent's resources.
func FromContext(ctx context.Context) (Identity, error) {
	ident := Identity{
		UserID:  UserIDFromContext(ctx),
		AgentID: AgentIDFromContext(ctx),
	}
	if ident.UserID == "" {
		return Identity{}, ErrUnauthenticated
	}
	ident.AgentScoped = ident.AgentID != ""
	return ident, nil
}

func IsNotFound(err error) bool { return errors.Is(err, ErrNotFound) }

func IsForbidden(err error) bool { return errors.Is(err, ErrForbidden) }
