package library

import (
	"errors"
	"fmt"
	"time"
)

// Scope is the owner scope shared by Library management and retrieval.
type Scope string

const (
	ScopeSystem      Scope = "system"
	ScopeSystemAgent Scope = "system_agent"
	ScopeUser        Scope = "user"
	ScopeUserAgent   Scope = "user_agent"
)

// FileStatus is the durable processing state of one immutable snapshot.
type FileStatus string

const (
	FileStatusProcessing FileStatus = "processing"
	FileStatusReady      FileStatus = "ready"
	FileStatusFailed     FileStatus = "failed"
)

// ChunkSetStatus is the publication state of one deterministic generation.
// Only a ready set referenced by LibraryFile.ActiveChunkSetID may be retrieved.
type ChunkSetStatus string

const (
	ChunkSetStatusBuilding ChunkSetStatus = "building"
	ChunkSetStatusReady    ChunkSetStatus = "ready"
	ChunkSetStatusFailed   ChunkSetStatus = "failed"
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

var (
	ErrInvalidOwner        = errors.New("invalid library owner")
	ErrFileTooLarge        = errors.New("library file is too large")
	ErrUnsupportedFileType = errors.New("unsupported library file type")
	ErrInvalidFile         = errors.New("invalid library file")
	ErrQuotaExceeded       = errors.New("library quota exceeded")
	ErrNotFound            = errors.New("library file not found")
	ErrForbidden           = errors.New("library access forbidden")
	ErrServiceUnavailable  = errors.New("library service is unavailable")
	ErrSpoolCapacity       = errors.New("library upload spool is at capacity")
	ErrGenerationConflict  = errors.New("library chunk generation identity conflicts with durable state")
	ErrGenerationChanged   = errors.New("library chunk generation state changed")
	ErrRawIntegrity        = errors.New("library raw snapshot failed integrity validation")
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

// LibraryFile is internal metadata for one canonical immutable source snapshot.
// Raw bytes remain in RawStore and are never included in this value.
type LibraryFile struct {
	ID               string
	Owner            Owner
	FileName         string
	MediaType        string
	SizeBytes        int64
	RawSHA256        []byte
	Status           FileStatus
	ErrorMessage     string
	ActiveChunkSetID string
	DeletedAt        *time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// ListCursor is the stable position after one management-list item.
// HTTP keeps its serialized form opaque and binds it to the authorized query.
type ListCursor struct {
	CreatedAt time.Time
	ID        string
}

// Quota describes current usage and the fixed limit of one logical quota pool.
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

// QuotaExceededError carries the authoritative pool state at rejection time.
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
