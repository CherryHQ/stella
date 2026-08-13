// Package memory defines the pluggable memory provider contract.
//
// Every memory plugin implements [Provider] as its core interface.
// Optional capabilities are discovered via type assertion at runtime,
// following the same pattern as [github.com/CherryHQ/stella/pkg/hooks].
package memory

import (
	"context"
	"errors"
	"time"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/pkg/ai"
)

// SessionStats contains basic statistics about a session's memory state.
// Every provider can produce this since it only requires knowledge of
// what was stored via Append.
type SessionStats struct {
	MessageCount int       // total messages stored for this session
	TokenCount   int       // estimated total tokens across all stored messages
	SummaryCount int       // number of summaries (0 for providers without Compactor)
	OldestAt     time.Time // timestamp of the earliest message (zero if empty)
	NewestAt     time.Time // timestamp of the most recent message (zero if empty)
}

// ErrInboxNotPending reports that an inbox-backed input could not claim its
// pending row. Cancellation, prior delivery, and immutable-fact mismatch all
// fail closed through this one sentinel before model execution.
var ErrInboxNotPending = errors.New("session inbox message is not pending")

// ErrInboxAppendOutcomeUnknown reports a commit-acknowledgement failure after
// the inbox claim and transcript writes were sent. The transaction may have
// committed; callers must resolve the pending row before promising no delivery.
var ErrInboxAppendOutcomeUnknown = errors.New("session inbox append outcome unknown")

// InboxAppender is implemented by providers that can atomically claim a durable
// Session inbox row and append its input to the transcript. Callers must verify
// the unwrapped provider supports this capability before relying on a wrapper.
type InboxAppender interface {
	AppendInboxInput(ctx context.Context, session Session, inboxID string, msg ai.Message) error
}

// Provider is the memory plugin contract.
// Every memory plugin must implement this interface.
// Optional capabilities are discovered via type assertion — see Capability Interfaces.
type Provider interface {
	// Name returns the plugin identifier (e.g. "lcm", "simple").
	// Used in logs and admin UI.
	Name() string

	// Bootstrap ensures the session is initialized and ready for use.
	// Called once at the start of every chat turn before any Append or Assemble.
	// Implementations use this to create conversation records, initialize caches,
	// or establish remote connections for the session.
	Bootstrap(ctx context.Context, session Session) error

	// Append persists one or more messages to the session's event log.
	// Ordinary sessions receive canonical image references and must reject raw
	// provider image bytes. Group sessions may retain their legacy inline codec.
	// Messages must be appended in the order they arrive.
	// Callers pass all messages from a single turn together so implementations
	// can store them in a single atomic transaction if they choose.
	//
	// Concurrency: implementations MUST be safe for concurrent Append calls
	// on different sessions. Concurrent Append calls on the SAME session
	// MUST be serialised by the implementation (see Concurrency Contract).
	Append(ctx context.Context, session Session, msgs ...ai.Message) error

	// Assemble builds the context window to send to the LLM.
	// budget: maximum number of tokens the returned messages may consume.
	// freshTail: number of recent user turns to prefer verbatim. Implementations
	//   may apply safety caps so oversized tails do not consume the whole budget.
	// Returns messages in chronological order (oldest first).
	// Older content that does not fit in the budget is either summarised
	// (if the plugin supports Compactor) or omitted.
	Assemble(ctx context.Context, session Session, budget, freshTail int) ([]ai.Message, error)

	// Stats returns basic statistics about a session's memory state.
	// Used by the memory tool's "status" action and by admin endpoints.
	// Returns zero-value stats (not an error) if the session does not exist.
	Stats(ctx context.Context, session Session) (SessionStats, error)

	// Close releases any resources held by the provider (DB connections, caches, etc.).
	// Called on shutdown. Must be safe to call multiple times.
	Close() error
}

// ---------------------------------------------------------------------------
// Capability: Compactor
// ---------------------------------------------------------------------------

// CompactionMode controls compaction behavior.
type CompactionMode int

const (
	// CompactionIncremental runs one leaf pass + one condensed pass.
	CompactionIncremental CompactionMode = iota
	// CompactionFull repeats until no more compaction is possible.
	CompactionFull
)

// String returns the human-readable name of the compaction mode.
func (m CompactionMode) String() string {
	switch m {
	case CompactionIncremental:
		return "incremental"
	case CompactionFull:
		return "full"
	default:
		return "unknown"
	}
}

// CompactionResult reports what a compaction cycle accomplished.
type CompactionResult struct {
	LeafSummariesCreated      int
	CondensedSummariesCreated int
	MessagesCompacted         int
	TokensBefore              int
	TokensAfter               int
	Duration                  time.Duration
}

// Compactor is implemented by providers that support background compaction.
// The runtime calls NeedsCompaction before each chat turn and Compact when needed.
type Compactor interface {
	// NeedsCompaction returns true if the session's context token count exceeds
	// threshold, an absolute token count.
	NeedsCompaction(ctx context.Context, session Session, threshold float64) bool

	// Compact runs the compaction algorithm on the session.
	// Incremental mode runs a single summarisation pass (fast, called automatically).
	// Full mode runs repeated passes until no further reduction is possible
	// (slow, called on demand e.g. via /compact slash command).
	Compact(ctx context.Context, session Session, mode CompactionMode) (*CompactionResult, error)
}

// ---------------------------------------------------------------------------
// Capability: Searcher
// ---------------------------------------------------------------------------

// SearchScope controls which storage layer to search.
type SearchScope int

const (
	SearchScopeBoth      SearchScope = iota // default: search everything
	SearchScopeMessages                     // raw messages only
	SearchScopeSummaries                    // summaries only
)

// SearchQuery describes a search request.
type SearchQuery struct {
	Text  string      // search term (keyword or natural language depending on plugin)
	Scope SearchScope // which layer of storage to search
	Limit int         // max results (default 20)
}

// SearchResult represents a single search hit. Results span every active
// session of the current (user_id, agent_id), so each hit carries provenance —
// which conversation it came from — plus the time the content actually occurred
// so the agent can weight recency.
type SearchResult struct {
	SourceType        string    `json:"source_type"`        // "message" or "summary"
	SourceID          string    `json:"source_id"`          // message ID or summary ID
	Content           string    `json:"content"`            // snippet of the matching content (truncated at ~500 chars)
	Score             float64   `json:"score"`              // relevance, higher is better: pg_search BM25 for a single-scope query, RRF fusion score when messages and summaries are merged
	OccurredAt        time.Time `json:"occurred_at"`        // when the underlying content actually happened
	SessionID         string    `json:"session_id"`         // origin session for traceability
	ConversationTitle string    `json:"conversation_title"` // human-readable origin label (may be empty)
}

// Searcher is implemented by providers that support history search.
type Searcher interface {
	Search(ctx context.Context, session Session, query SearchQuery) ([]SearchResult, error)
}

// RecallReference identifies one conversation-memory resource behind the
// Session policy boundary. It is an internal transport value: model-facing
// callers receive an opaque encoded ref and every read reauthorizes this tuple.
type RecallReference struct {
	Kind      string
	ID        string
	SessionID string
}

// RecallSearchResult is one authorized message or summary match. Source kinds
// stay inside the recall facade; the memory tool returns one uniform result
// shape and an opaque ref instead.
type RecallSearchResult struct {
	Reference         RecallReference
	Content           string
	Score             float64
	OccurredAt        time.Time
	SessionID         string
	ConversationTitle string
}

// RecallFragment is one authorized, one-level expansion item for a summary.
type RecallFragment struct {
	Reference  RecallReference
	Role       string
	Authority  string
	Kind       string
	Depth      *int
	Content    string
	OccurredAt time.Time
}

// RecallSummaryDetail preserves LCM's describe and bounded one-level expand
// capabilities without exposing provider actions to the model.
type RecallSummaryDetail struct {
	Kind            string
	Depth           int
	DescendantCount int
	EarliestAt      *time.Time
	LatestAt        *time.Time
	Parents         []RecallReference
	Children        []RecallReference
	Expanded        []RecallFragment
}

// RecallDocument is one fully authorized conversation-memory read.
type RecallDocument struct {
	Reference         RecallReference
	Content           string
	Role              string
	Authority         string
	OccurredAt        time.Time
	SessionID         string
	ConversationTitle string
	Summary           *RecallSummaryDetail
}

// RecallSource is the authorized Session facade used by the model-facing
// memory tool. It keeps transcript policy in Session while memory presents one
// search/read mental model across transcripts and durable memory.
type RecallSource interface {
	SearchRecall(ctx context.Context, authority authz.Authority, agentID, query string, limit int) ([]RecallSearchResult, error)
	ReadRecall(ctx context.Context, authority authz.Authority, agentID string, ref RecallReference, tokenCap int) (RecallDocument, error)
}

// ---------------------------------------------------------------------------
// Capability: Explorer
// ---------------------------------------------------------------------------

// DescribeResult represents summary metadata and lineage.
type DescribeResult struct {
	SummaryID       string
	Kind            string     // "leaf" or "condensed"
	Depth           int        // 0 = leaf, 1+ = condensed
	Content         string     // the summary text
	EarliestAt      *time.Time // timestamp of oldest source message
	LatestAt        *time.Time // timestamp of newest source message
	DescendantCount int        // total original messages this summary covers
	ParentIDs       []string   // summaries that contain this one (condensed parents)
	ChildIDs        []string   // summaries or messages this one was built from
}

// ExpandResult represents drill-down results from exploring a summary.
type ExpandResult struct {
	SummaryID string
	// For leaf summaries: the original source messages.
	Messages []ExpandMessage
	// For condensed summaries: the child summaries.
	Children []ExpandChild
}

// ExpandMessage is a source message in an expand result.
type ExpandMessage struct {
	MessageID string
	Role      string
	Content   string
	CreatedAt time.Time
}

// ExpandChild is a child summary in an expand result.
type ExpandChild struct {
	SummaryID string
	Kind      string
	Depth     int
	Content   string
}

// Explorer is implemented by providers that store summaries in a navigable hierarchy.
// It lets the agent inspect and drill into compressed history.
//
// Note: these methods take summaryID only, not Session. Summary IDs are globally
// unique (e.g. "sum_a1b2c3d4") and already scoped to a conversation internally.
type Explorer interface {
	// Describe returns metadata and lineage for a summary ID.
	Describe(ctx context.Context, summaryID string) (*DescribeResult, error)

	// Expand drills into a summary:
	//   - For leaf summaries: returns the original source messages
	//   - For condensed summaries: returns the child summaries
	// tokenCap limits how many tokens of content are returned.
	Expand(ctx context.Context, summaryID string, tokenCap int) (*ExpandResult, error)
}

// ---------------------------------------------------------------------------
// Capability: MessageReader
// ---------------------------------------------------------------------------

// MessageDetail is the full, untruncated content of a single stored message.
type MessageDetail struct {
	MessageID         string    `json:"message_id"`
	Role              string    `json:"role"`
	Content           string    `json:"content"`
	OccurredAt        time.Time `json:"occurred_at"`        // when the message happened
	SessionID         string    `json:"session_id"`         // origin session for traceability
	ConversationTitle string    `json:"conversation_title"` // human-readable origin label (may be empty)
}

// MessageReader is implemented by providers that can return a single message in
// full by ID, scoped to (user_id, agent_id). It complements Searcher: search
// hits are truncated snippets, so the agent calls GetMessage to read a hit in
// full — including hits from other sessions of the same user+agent.
type MessageReader interface {
	GetMessage(ctx context.Context, messageID string) (*MessageDetail, error)
}

// ---------------------------------------------------------------------------
// Capability: ProfileStore
// ---------------------------------------------------------------------------

// ProfileStore is implemented by providers that support per-user-per-agent
// persistent memory: agent soul (identity/personality customization) and
// user profile (facts/context about the user).
//
// Both are scoped to (userID, agentID) — each user can customise the agent's
// soul independently, and the agent maintains separate profile notes per user.
// Content is free-form text managed entirely by the agent. The system injects
// both into the system prompt at session start.
type ProfileStore interface {
	// GetProfile returns the current user profile for the (userID, agentID) pair.
	// Returns ("", nil) if no profile exists yet (not an error).
	GetProfile(ctx context.Context, userID string, agentID string) (string, error)

	// SetProfile overwrites the user profile for the (userID, agentID) pair.
	// Callers are responsible for merging new content with existing content
	// before calling SetProfile — this method always replaces, never appends.
	SetProfile(ctx context.Context, userID string, agentID string, content string) error

	// GetAgentSoul returns the agent soul for the (userID, agentID) pair.
	// The soul defines the agent's identity, personality, and behavior as
	// customised by this specific user. Returns ("", nil) if not set.
	GetAgentSoul(ctx context.Context, userID string, agentID string) (string, error)

	// SetAgentSoul overwrites the agent soul for the (userID, agentID) pair.
	// Callers are responsible for merging; this method always replaces.
	SetAgentSoul(ctx context.Context, userID string, agentID string, content string) error
}

// ---------------------------------------------------------------------------
// Capability: ProfileEntry (D5 dated entries)
// ---------------------------------------------------------------------------

// ProfileEntry represents a single dated profile entry, either manually edited
// by the user or auto-generated by async memory ingest (D6).
type ProfileEntry struct {
	ID        string `json:"id"`
	Text      string `json:"text"`
	Source    string `json:"source"`     // "manual" or "auto"
	CreatedAt string `json:"created_at"` // RFC3339
}

// ProfileEntryStore is implemented by providers that support dated profile
// entries (D5). Auto-generated entries live alongside the manual profile blob.
type ProfileEntryStore interface {
	GetProfileEntries(ctx context.Context, userID string, agentID string) ([]ProfileEntry, error)
}

// ---------------------------------------------------------------------------
// Capability: GroupMemoryStore
// ---------------------------------------------------------------------------

// GroupMemoryStore provides read access to group-scoped shared memory.
// Write access is through the memorywrite package (type-level isolation).
type GroupMemoryStore interface {
	GetGroupMemory(ctx context.Context, groupID string) (string, error)
}

// ---------------------------------------------------------------------------
// Capability: ConstraintStore
// ---------------------------------------------------------------------------

// ConstraintEntry represents a single user constraint.
type ConstraintEntry struct {
	ID        string `json:"id"`
	Text      string `json:"text"`
	CreatedAt string `json:"created_at"`
}

// ConstraintStore is implemented by providers that support storing user-defined
// hard constraints (rules the agent must always follow).
// Constraints are separate from Profile to give them special protection:
// Reflect cannot modify them; only user-initiated writes are allowed by convention.
type ConstraintStore interface {
	// GetConstraints returns all active constraints for the (userID, agentID) pair.
	GetConstraints(ctx context.Context, userID string, agentID string) ([]ConstraintEntry, error)

	// AddConstraint adds a new constraint with a generated UUID-like ID.
	// Returns the updated list of constraints.
	AddConstraint(ctx context.Context, userID string, agentID string, text string) ([]ConstraintEntry, error)

	// RemoveConstraint removes the constraint with the given ID.
	// Returns the updated list of constraints.
	RemoveConstraint(ctx context.Context, userID string, agentID string, id string) ([]ConstraintEntry, error)
}

// ---------------------------------------------------------------------------
// Capability: SessionManager
// ---------------------------------------------------------------------------

// ErrStaleRotation reports that the session a rotation expected to replace is no
// longer the active one — a concurrent rotation already won. Rotation is a
// compare-and-rotate, so a stale caller changed nothing and must not retry.
var ErrStaleRotation = errors.New("session rotation is stale")

// ErrInactiveSession reports that a metadata write lost a race with archival
// (or the scoped session no longer exists). Callers must not retry the stale
// snapshot because doing so could revive lifecycle state that has moved on.
var ErrInactiveSession = errors.New("session is not active")

// ErrRotationOutcomeUnknown reports that a rotation failed at the commit
// acknowledgement: the server may or may not have committed (e.g. the
// connection to an external PostgreSQL dropped after COMMIT was sent). Every
// other rotation failure is a definite rollback; this one is not, so a caller
// compensating for "rotation never happened" — releasing a dedup claim,
// auto-retrying — must treat it as possibly-happened instead.
var ErrRotationOutcomeUnknown = errors.New("session rotation outcome unknown")

// SessionManager is implemented by providers that support session lifecycle management.
type SessionManager interface {
	// SaveInfo creates a session or updates metadata while it remains active.
	// Archived is only honored when creating a new row; lifecycle transitions
	// for an existing row must use ArchiveInfo or RotateInfo.
	SaveInfo(ctx context.Context, info SessionInfo) error

	// ArchiveInfo atomically transitions an active session to archived. It
	// reports false without writing when the scoped session is already inactive.
	ArchiveInfo(ctx context.Context, info SessionInfo) (bool, error)

	// RotateInfo archives expectedSessionID and creates successor as one atomic
	// unit, so a durable binding is never left without an active session and a
	// one-active-session-per-binding index is never violated mid-rotation.
	// It returns ErrStaleRotation, with nothing persisted, when expectedSessionID
	// is no longer active or no longer matches successor's binding.
	RotateInfo(ctx context.Context, expectedSessionID string, successor SessionInfo) error

	// TouchActiveInfo records a running turn's metadata — its last-active
	// timestamp, and a title for a session that has none — against a session that
	// is still active, and reports false without writing when it is not.
	//
	// It exists because SaveInfo cannot serve the turn path: a turn holds the
	// snapshot it resolved at the start, a rotation can archive that session
	// while the turn runs, and replaying the snapshot would un-archive a session
	// the chat has already left. Checking first and then saving only narrows the
	// race; this is the guard and the write in one statement.
	TouchActiveInfo(ctx context.Context, info SessionInfo) (bool, error)

	// LoadInfo retrieves metadata for a single session.
	LoadInfo(ctx context.Context, sessionID string) (SessionInfo, error)

	// ListInfo lists sessions matching the options.
	ListInfo(ctx context.Context, opts ListOptions) ([]SessionInfo, error)

	// LoadHistory returns the complete raw message history for a session
	// in chronological order. Used by the Web UI viewer and export.
	LoadHistory(ctx context.Context, sessionID string) ([]ai.Message, error)
}

// SessionActivityStore persists the latest terminal turn result and view
// watermark. Working remains process-local runtime truth; unread terminal
// results survive navigation and refresh until the session is opened.
type SessionActivityStore interface {
	MarkSessionTurnStarted(ctx context.Context, session Session) (bool, error)
	MarkSessionTurnCompleted(ctx context.Context, session Session, result SessionTurnResult) (bool, error)
	MarkSessionViewed(ctx context.Context, session Session) (bool, error)
}

// ReviewMessage is a logical message with the underlying storage boundary kept
// intact for deterministic review watermarking. Assistant turns may span
// multiple storage rows, so callers must advance to LastSeq after reviewing one.
type ReviewMessage struct {
	ID       string
	FirstSeq int64
	LastSeq  int64
	Message  ai.Message
}

// ReviewHistoryReader returns raw session history with stable storage
// boundaries for background reviewers.
type ReviewHistoryReader interface {
	LoadReviewHistory(ctx context.Context, sessionID string) ([]ReviewMessage, error)
}

// ---------------------------------------------------------------------------
// Capability: Reviewer
// ---------------------------------------------------------------------------

// Reviewer is implemented by providers that can format conversation content
// for review by a background agent (the reflect system).
//
// This is deliberately a single method. Watermark tracking (which sessions
// have been reviewed, when) is not the memory plugin's concern — it belongs
// to the consumer (`internal/reflect`). The memory provider's only job is:
// "given a session and an optional time boundary, produce reviewable text."
type Reviewer interface {
	// BuildReviewContext returns a text representation of the conversation
	// suitable for passing to a reviewer agent's prompt.
	//
	// since: if non-zero, only include content created after this time.
	//   The provider should include prior context (e.g. summaries from before
	//   this timestamp) to give the reviewer enough background.
	//   If zero, include all content.
	//
	// Returns ("", nil) if there is no content to review.
	BuildReviewContext(ctx context.Context, session Session, since time.Time) (string, error)
}
