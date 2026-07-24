package knowledge

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Scope is the owner scope shared by Knowledge management and retrieval.
type Scope string

const (
	ScopeSystem      Scope = "system"
	ScopeSystemAgent Scope = "system_agent"
	ScopeUser        Scope = "user"
	ScopeUserAgent   Scope = "user_agent"
)

// FileStatus is the durable ingestion state of one immutable file.
type FileStatus string

const (
	FileStatusProcessing FileStatus = "processing"
	FileStatusReady      FileStatus = "ready"
	FileStatusFailed     FileStatus = "failed"
)

const (
	MaxFileBytes = 25 << 20

	SystemMaxFiles      int64 = 4_000
	SystemMaxBytes      int64 = 20 << 30
	SystemAgentMaxFiles int64 = 1_000
	SystemAgentMaxBytes int64 = 5 << 30
	PersonalMaxFiles    int64 = 2_000
	PersonalMaxBytes    int64 = 10 << 30
)

// MaxSearchQueryRunes bounds one model-generated BM25 query without penalizing
// Chinese text, where byte length is not a useful user-facing measure.
const MaxSearchQueryRunes = 500

var (
	ErrInvalidOwner        = errors.New("invalid knowledge owner")
	ErrFileTooLarge        = errors.New("knowledge file is too large")
	ErrUnsupportedFileType = errors.New("unsupported knowledge file type")
	ErrInvalidFile         = errors.New("invalid knowledge file")
	ErrQuotaExceeded       = errors.New("knowledge quota exceeded")
	ErrNotFound            = errors.New("knowledge file not found")
	ErrForbidden           = errors.New("knowledge access forbidden")
	ErrServiceUnavailable  = errors.New("knowledge service is unavailable")
)

// Owner is the normalized four-part scope tuple. Empty IDs are database NULLs.
type Owner struct {
	Scope   Scope
	UserID  string
	AgentID string
}

// Validate enforces the same four legal combinations as the database CHECK.
func (o Owner) Validate() error {
	switch o.Scope {
	case ScopeSystem:
		if o.UserID == "" && o.AgentID == "" {
			return nil
		}
	case ScopeSystemAgent:
		if o.UserID == "" && o.AgentID != "" {
			return nil
		}
	case ScopeUser:
		if o.UserID != "" && o.AgentID == "" {
			return nil
		}
	case ScopeUserAgent:
		if o.UserID != "" && o.AgentID != "" {
			return nil
		}
	}
	return fmt.Errorf(
		"%w: scope=%q user_set=%t agent_set=%t",
		ErrInvalidOwner,
		o.Scope,
		o.UserID != "",
		o.AgentID != "",
	)
}

// File is management metadata for one immutable source document.
type File struct {
	ID           string
	Owner        Owner
	FileName     string
	MediaType    string
	SizeBytes    int64
	Status       FileStatus
	ErrorMessage string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Quota describes current usage and the fixed limit of one quota pool.
type Quota struct {
	UsedFiles int64
	MaxFiles  int64
	UsedBytes int64
	MaxBytes  int64
}

// CanAdd reports whether adding one immutable file stays inside both limits.
func (q Quota) CanAdd(sizeBytes int64) bool {
	return q.UsedFiles+1 <= q.MaxFiles && q.UsedBytes+sizeBytes <= q.MaxBytes
}

// QuotaExceededError carries the pool state used by the API's 429 response.
type QuotaExceededError struct {
	Quota Quota
}

func (e *QuotaExceededError) Error() string {
	return fmt.Sprintf(
		"%v: files %d/%d, bytes %d/%d",
		ErrQuotaExceeded,
		e.Quota.UsedFiles,
		e.Quota.MaxFiles,
		e.Quota.UsedBytes,
		e.Quota.MaxBytes,
	)
}

func (e *QuotaExceededError) Unwrap() error { return ErrQuotaExceeded }

// ListCursor is the keyset position used by the management API.
type ListCursor struct {
	CreatedAt time.Time
	ID        string
}

// ListInput identifies one owner tuple and one stable list page.
type ListInput struct {
	Owner    Owner
	Query    string
	PageSize int
	Cursor   *ListCursor
}

// ListResult contains one metadata page plus quota for the full pool.
type ListResult struct {
	Files   []File
	HasMore bool
	Quota   Quota
}

// SearchResult is the only evidence shape returned by the Agent tool.
type SearchResult struct {
	Content  string         `json:"content"`
	FileName string         `json:"file_name"`
	Locator  *PublicLocator `json:"locator,omitempty"`
}

// PublicLocator contains only stable positions that can be cited to a user.
type PublicLocator struct {
	FirstPage      *uint32 `json:"first_page,omitempty"`
	LastPage       *uint32 `json:"last_page,omitempty"`
	HeadingContext string  `json:"heading_context,omitempty"`
}

func normalizeQuery(value string) string {
	return strings.TrimSpace(value)
}
