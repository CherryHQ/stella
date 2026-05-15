package memory

import "context"

// ChangeSource identifies who triggered a memory write.
type ChangeSource string

const (
	SourceUser    ChangeSource = "user"
	SourceAgent   ChangeSource = "agent"
	SourceReflect ChangeSource = "reflect"
	SourceSystem  ChangeSource = "system"
)

// ChangeEntry represents one changelog record.
type ChangeEntry struct {
	ID                  string
	UserID              string
	AgentID             string
	SessionID           string
	Scope               string // "profile", "soul", "constraint", "skill", "compaction"
	Action              string // "create", "update", "delete", "compact"
	Source              ChangeSource
	MemoryVersionBefore *int64
	MemoryVersionAfter  *int64
	BeforeText          string
	AfterText           string
	CreatedAt           string
}

// ChangelogWriter is implemented by providers that record memory write history.
type ChangelogWriter interface {
	// WriteChangelog records a memory change.
	WriteChangelog(ctx context.Context, entry ChangeEntry) error
}

// ChangelogReader is implemented by providers that expose memory write history.
type ChangelogReader interface {
	// ReadChangelog returns the last N changelog entries for a given scope.
	ReadChangelog(ctx context.Context, userID string, agentID string, scope string, limit int) ([]ChangeEntry, error)
}

type changeSourceKey struct{}

// WithChangeSource stores a ChangeSource in the context.
// This lets callers (e.g. Reflect) tag writes without changing function signatures.
func WithChangeSource(ctx context.Context, source ChangeSource) context.Context {
	return context.WithValue(ctx, changeSourceKey{}, source)
}

// ChangeSourceFromContext retrieves the ChangeSource from context.
// Returns SourceAgent if not set.
func ChangeSourceFromContext(ctx context.Context) ChangeSource {
	if s, ok := ctx.Value(changeSourceKey{}).(ChangeSource); ok {
		return s
	}
	return SourceAgent
}
