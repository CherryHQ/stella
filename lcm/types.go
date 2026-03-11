package lcm

import (
	"context"
	"time"

	"github.com/vaayne/anna/agent/runner"
)

// Summary kind constants.
const (
	KindLeaf      = "leaf"
	KindCondensed = "condensed"
)

// Message role constants.
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
)

// MessagePart type constants.
const (
	PartTypeText      = "text"
	PartTypeReasoning = "reasoning"
	PartTypeTool      = "tool"
)

// Context item type constants.
const (
	ItemTypeMessage = "message"
	ItemTypeSummary = "summary"
)

// Default configuration values.
const (
	DefaultFreshTail        = 20
	DefaultContextThreshold = 0.75
	DefaultLeafChunkSize    = 10 // messages per leaf summary
)

// Engine is the main LCM interface for lossless context management.
type Engine interface {
	// Bootstrap reconciles session state on startup.
	Bootstrap(ctx context.Context, sessionID string) error

	// Ingest persists a message and appends to context.
	Ingest(ctx context.Context, sessionID string, evt runner.RPCEvent) error

	// IngestBatch persists multiple messages.
	IngestBatch(ctx context.Context, sessionID string, evts []runner.RPCEvent) error

	// Assemble builds context for the model within token budget.
	// Returns []runner.RPCEvent for compatibility with the existing runner pipeline.
	Assemble(ctx context.Context, sessionID string, budget int, freshTail int) ([]runner.RPCEvent, error)

	// Compact runs compaction passes (leaf + optional condensation).
	Compact(ctx context.Context, sessionID string, mode CompactionMode) (*CompactionResult, error)

	// NeedsCompaction checks if compaction should run based on token threshold.
	NeedsCompaction(ctx context.Context, sessionID string, threshold float64) bool

	// Retrieval returns the retrieval engine for tools.
	Retrieval() *RetrievalEngine

	// Close releases database resources.
	Close() error
}

// Summarizer generates summaries from content.
type Summarizer interface {
	Summarize(ctx context.Context, text string, opts SummarizeOptions) (string, error)
}

// SummarizeOptions controls summarization behavior.
type SummarizeOptions struct {
	IsCondensed  bool
	Depth        int
	Aggressive   bool
	Previous     string // previous summary for continuity
	TargetTokens int
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

// CompactionMode controls compaction behavior.
type CompactionMode int

const (
	// CompactionIncremental runs a single leaf pass + optional condensation.
	CompactionIncremental CompactionMode = iota
	// CompactionFull runs repeated passes until no more compaction is possible.
	CompactionFull
)

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

// RetrievalEngine provides search and exploration of compacted history.
type RetrievalEngine struct {
	q *Queries
}

// GrepResult represents a single search hit from memory_grep.
type GrepResult struct {
	SourceType string // "message" or "summary"
	SourceID   string
	Content    string
	Relevance  float64 // reserved for future scoring; currently 0
	Timestamp  time.Time
}

// DescribeResult represents summary metadata from memory_describe.
type DescribeResult struct {
	SummaryID       string
	Kind            string
	Depth           int
	Content         string
	EarliestAt      *time.Time
	LatestAt        *time.Time
	DescendantCount int
	ParentIDs       []string
	ChildIDs        []string
}

// ExpandResult represents drill-down results from memory_expand.
type ExpandResult struct {
	SummaryID string
	Children  []ExpandChild
	Messages  []ExpandMessage
}

// ExpandChild is a child summary in an expand result.
type ExpandChild struct {
	SummaryID string
	Kind      string
	Depth     int
	Content   string
}

// ExpandMessage is a source message in an expand result.
type ExpandMessage struct {
	MessageID int64
	Role      string
	Content   string
	CreatedAt time.Time
}

// EstimateTokens returns a rough token count (~4 chars per token).
// This matches the existing estimator in agent/store/store.go.
func EstimateTokens(text string) int {
	return (len(text) + 3) / 4
}
