package memory

import (
	"context"
	"fmt"
	"time"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/pkg/ai"
)

// ScopeUserIDFromContext returns the user_id this turn's conversation rows are
// keyed by. A group turn carries no user identity — runtime identity stays the
// group (D9) — and guest turns likewise carry no Stella user identity. Their
// conversations persist under their group_id or guest_id compatibility owner
// key. Use this for conversation-scoped reads and writes (session info,
// messages, summaries) only. Per-user data — soul, profile, constraints,
// knowledge facts — must keep resolving strictly against
// authz.UserIDFromContext so a group or guest turn fails closed instead of
// reading a person's private rows.
func ScopeUserIDFromContext(ctx context.Context) string {
	if userID := authz.UserIDFromContext(ctx); userID != "" {
		return userID
	}
	if guestID := authz.GuestIDFromContext(ctx); guestID != "" {
		return guestID
	}
	return authz.GroupIDFromContext(ctx)
}

type contextKey string

const (
	sessionIDKey      contextKey = "memory_session_id"
	projectIDKey      contextKey = "memory_project_id"
	groupSeqKey       contextKey = "memory_group_seq"
	groupMessageIDKey contextKey = "memory_group_message_id"
	currentSpeakerKey contextKey = "memory_current_speaker"
)

// WithSessionID attaches a session ID to the context.
func WithSessionID(ctx context.Context, sessionID string) context.Context {
	return context.WithValue(ctx, sessionIDKey, sessionID)
}

// SessionIDFromContext extracts the session ID from context.
func SessionIDFromContext(ctx context.Context) string {
	s, _ := ctx.Value(sessionIDKey).(string)
	return s
}

// WithProjectID attaches a project ID to the context.
func WithProjectID(ctx context.Context, projectID string) context.Context {
	return context.WithValue(ctx, projectIDKey, projectID)
}

// ProjectIDFromContext extracts the project ID from context.
func ProjectIDFromContext(ctx context.Context) string {
	s, _ := ctx.Value(projectIDKey).(string)
	return s
}

// CurrentSpeaker carries the human who sent the current turn in a group session,
// where the runtime/session identity is the group (D9), not any one person.
//
// D9 WARNING: CurrentSpeaker.UserID is a per-turn personalization target ONLY,
// never a runtime identity. Do not pass it to WithUserID, sandbox config,
// vault/token code, plugin contexts, delegate sessions, notify routing, or hook
// user metadata. Session/runtime ownership keeps flowing from the group id;
// this axis only selects whose profile the prompt and memory tool personalize.
type CurrentSpeaker struct {
	Platform       string // source platform (telegram, feishu, web, ...)
	PlatformUserID string // platform sender id / Web auth user id; lookup/audit only
	DisplayName    string // sender display name when available
	UserID         string // resolved Stella auth user id when linked; empty when unlinked
}

// WithCurrentSpeaker attaches the current-turn speaker to the context.
func WithCurrentSpeaker(ctx context.Context, speaker CurrentSpeaker) context.Context {
	return context.WithValue(ctx, currentSpeakerKey, speaker)
}

// CurrentSpeakerFromContext extracts the current-turn speaker. The bool is false
// when no speaker is set (DM turns); it is true but UserID may be empty for an
// unlinked platform sender.
func CurrentSpeakerFromContext(ctx context.Context) (CurrentSpeaker, bool) {
	s, ok := ctx.Value(currentSpeakerKey).(CurrentSpeaker)
	return s, ok
}

// WithGroupSeq attaches the triggering event-log seq to the context.
func WithGroupSeq(ctx context.Context, seq int64) context.Context {
	return context.WithValue(ctx, groupSeqKey, seq)
}

// GroupSeqFromContext extracts the triggering event-log seq from context.
func GroupSeqFromContext(ctx context.Context) int64 {
	s, _ := ctx.Value(groupSeqKey).(int64)
	return s
}

// WithGroupMessageID attaches the triggering public event-log message ID.
func WithGroupMessageID(ctx context.Context, messageID string) context.Context {
	return context.WithValue(ctx, groupMessageIDKey, messageID)
}

// GroupMessageIDFromContext extracts the triggering public event-log message ID.
func GroupMessageIDFromContext(ctx context.Context) string {
	s, _ := ctx.Value(groupMessageIDKey).(string)
	return s
}

// Session identifies the context of a single conversation.
// It is created by the runtime and passed to all Provider methods.
type Session struct {
	ID      string // unique session key (e.g. "default:cli:<uuid>:main")
	AgentID string // agent this session belongs to (e.g. "default")
	UserID  string // internal user ID (empty for anonymous/legacy)
	Channel string // originating channel (e.g. "cli", "telegram")
	GroupID string // non-empty for group sessions; assembles history from event log instead of ctx_message
	GuestID string // non-empty for a persistent channel guest; equals UserID
}

// GroupIngestPipeline names the ctx_group_ingest_cursor pipeline that tracks
// one agent's consumption of a group's event log. The LCM assembler owns the
// cursor's movement, but the name is shared: session rotation fast-forwards it
// as a message boundary, and the group dispatcher reads it to drop restarted
// dispatch rows whose trigger that boundary already consumed.
func GroupIngestPipeline(agentID string) string { return "lcm:" + agentID }

// GroupCursorCommitter advances group event-log ingestion only after a chat turn
// has completed successfully. Assemble may prepare between-turn rows, but commit
// owns durable cursor movement.
type GroupCursorCommitter interface {
	CommitGroupCursor(ctx context.Context, session Session, triggerSeq int64) error
}

// GroupEventIngestor keeps a per-agent group LCM synchronized with the public
// event log. SyncGroupEventsBefore persists prior public events before
// compaction; AppendGroupTurn atomically persists the current public trigger and
// the private assistant/tool continuation after a successful turn.
type GroupEventIngestor interface {
	SyncGroupEventsBefore(ctx context.Context, session Session, triggerSeq int64) error
	AppendGroupTurn(ctx context.Context, session Session, groupMessageID string, trigger ai.Message, continuation ...ai.Message) error
}

type SessionTurnResult string

const (
	SessionTurnSuccess  SessionTurnResult = "success"
	SessionTurnError    SessionTurnResult = "error"
	SessionTurnCanceled SessionTurnResult = "canceled"
)

func (r SessionTurnResult) Valid() bool {
	switch r {
	case SessionTurnSuccess, SessionTurnError, SessionTurnCanceled:
		return true
	default:
		return false
	}
}

// SessionInfo holds metadata about a session.
type SessionInfo struct {
	ID                  string
	AgentID             string
	UserID              string
	GroupID             string // non-empty for group sessions; runtime uses this to isolate identity surfaces
	GuestID             string // non-empty for a persistent channel guest; equals UserID
	Channel             string
	Kind                string // session kind: main, chat, scheduler, task
	ProjectID           string // set for project-scoped sessions
	Title               string // auto-generated from first message
	CreatedAt           time.Time
	LastActive          time.Time
	LastTurnStartedAt   time.Time
	LastTurnCompletedAt time.Time
	LastTurnResult      SessionTurnResult
	LastViewedAt        time.Time
	Archived            bool
	// LatestSeq is the latest persisted message sequence when supplied by a
	// review-aware listing. Zero means the provider has no stable sequence.
	LatestSeq int64
}

// ListOptions controls session listing filters.
type ListOptions struct {
	AgentID         string // filter by agent (empty = all)
	UserID          string // filter by user (empty = all)
	GuestID         string // filter by guest; empty excludes guest-owned sessions
	Kind            string // filter by kind (empty = all)
	Channel         string // filter by durable channel binding (empty = all)
	ProjectID       string // filter by project (empty = all)
	ProjectIDIsNull bool   // when true, require project_id IS NULL
	IncludeArchived bool
	ExcludeInternal bool // hide task/delegate worker sessions from user-facing lists
	Limit           int  // 0 = no limit
	Offset          int  // skip first N matching results
}

// EstimateTokens returns a rough token count (~4 chars per token).
func EstimateTokens(text string) int {
	return (len(text) + 3) / 4
}

// MessageText extracts the plain text content from an ai.Message.
// Returns the text for user/assistant messages, or the tool result for tool messages.
func MessageText(msg ai.Message) string {
	switch m := msg.(type) {
	case ai.UserMessage:
		switch c := m.Content.(type) {
		case string:
			return c
		case []ai.ContentBlock:
			return ai.FlattenText(c)
		default:
			return fmt.Sprintf("%v", m.Content)
		}
	case ai.AssistantMessage:
		return ai.FlattenText(m.Content)
	case ai.ToolResultMessage:
		return ai.FlattenText(m.Content)
	default:
		return ""
	}
}

// MessageRole returns the role string for an ai.Message.
func MessageRole(msg ai.Message) string {
	switch msg.(type) {
	case ai.UserMessage:
		return "user"
	case ai.AssistantMessage:
		return "assistant"
	case ai.ToolResultMessage:
		return "tool"
	default:
		return "unknown"
	}
}

// MessageTimestamp returns the timestamp of an ai.Message.
func MessageTimestamp(msg ai.Message) time.Time {
	switch m := msg.(type) {
	case ai.UserMessage:
		return m.Timestamp
	case ai.AssistantMessage:
		return m.Timestamp
	case ai.ToolResultMessage:
		return m.Timestamp
	default:
		return time.Time{}
	}
}
