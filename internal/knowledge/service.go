package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

const (
	defaultListPageSize = 20
	maxListPageSize     = 500
)

// Parser is the bounded document parser used by the asynchronous Worker.
type Parser interface {
	Parse(ctx context.Context, path, mediaType string) ([]ParsedChunk, error)
}

type parserAvailability interface {
	Available() error
}

// ServiceConfig contains the infrastructure shared by management, workers, and
// Agent retrieval.
type ServiceConfig struct {
	DB                     *pgxpool.Pool
	Parser                 Parser
	Logger                 *slog.Logger
	TempDir                string
	ReconciliationInterval time.Duration
	StaleAfter             time.Duration
	AgentAccess            *agentaccess.Service
}

// Service owns Knowledge persistence, authorization, and background parsing.
type Service struct {
	db          *pgxpool.Pool
	q           *sqlc.Queries
	parser      Parser
	logger      *slog.Logger
	agentAccess *agentaccess.Service

	tempDir                string
	reconciliationInterval time.Duration
	staleAfter             time.Duration
	river                  *river.Client[pgx.Tx]
}

// NewService constructs the shared Knowledge service.
func NewService(config ServiceConfig) (*Service, error) {
	if config.DB == nil {
		return nil, fmt.Errorf("knowledge database is required")
	}
	if config.Parser == nil {
		return nil, fmt.Errorf("knowledge parser is required")
	}
	logger := config.Logger
	if logger == nil {
		logger = slog.Default().With("component", "knowledge")
	}
	interval := config.ReconciliationInterval
	if interval <= 0 {
		interval = defaultReconciliationInterval
	}
	staleAfter := config.StaleAfter
	if staleAfter <= 0 {
		staleAfter = defaultStaleAfter
	}
	return &Service{
		db:                     config.DB,
		q:                      sqlc.New(config.DB),
		parser:                 config.Parser,
		logger:                 logger,
		agentAccess:            config.AgentAccess,
		tempDir:                config.TempDir,
		reconciliationInterval: interval,
		staleAfter:             staleAfter,
	}, nil
}

// SetRiverClient injects the process-wide working River client.
func (s *Service) SetRiverClient(client *river.Client[pgx.Tx]) {
	s.river = client
}

// Create validates the immutable upload, checks its quota pool under a
// transaction lock, stores it, and atomically inserts its parse job.
func (s *Service) Create(ctx context.Context, owner Owner, fileName string, content []byte) (File, error) {
	if err := owner.Validate(); err != nil {
		return File{}, err
	}
	if s == nil || s.db == nil || s.river == nil {
		return File{}, ErrServiceUnavailable
	}
	// Managed binaries are installed in the background during startup. Reject
	// a new upload before persisting it when the parser shim is not ready yet;
	// otherwise River could exhaust all attempts during that installation race.
	if availability, ok := s.parser.(parserAvailability); ok {
		if err := availability.Available(); err != nil {
			s.logger.Warn("knowledge parser is unavailable")
			return File{}, ErrServiceUnavailable
		}
	}
	upload, err := ValidateUpload(fileName, content)
	if err != nil {
		return File{}, err
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return File{}, fmt.Errorf("begin knowledge file creation: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	qtx := s.q.WithTx(tx)

	if err := qtx.LockKnowledgeQuotaPool(ctx, quotaLockKey(owner)); err != nil {
		return File{}, fmt.Errorf("lock knowledge quota: %w", err)
	}
	quota, err := quotaForOwner(ctx, qtx, owner)
	if err != nil {
		return File{}, err
	}
	if !quota.CanAdd(int64(len(upload.Content))) {
		return File{}, &QuotaExceededError{Quota: quota}
	}

	row, err := qtx.CreateKnowledgeFile(ctx, sqlc.CreateKnowledgeFileParams{
		Scope:      string(owner.Scope),
		UserID:     nullableText(owner.UserID),
		AgentID:    nullableText(owner.AgentID),
		FileName:   upload.FileName,
		MediaType:  upload.MediaType,
		SizeBytes:  int64(len(upload.Content)),
		RawContent: upload.Content,
	})
	if err != nil {
		return File{}, fmt.Errorf("create knowledge file: %w", err)
	}
	if _, err := s.river.InsertTx(ctx, tx, parseArgs{FileID: row.ID}, nil); err != nil {
		return File{}, fmt.Errorf("enqueue knowledge parse: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return File{}, fmt.Errorf("commit knowledge file creation: %w", err)
	}
	return fileFromCreateRow(row), nil
}

// Get returns management metadata only; raw bytes and chunks are never exposed.
func (s *Service) Get(ctx context.Context, id string) (File, error) {
	if s == nil || s.q == nil {
		return File{}, ErrServiceUnavailable
	}
	row, err := s.q.GetKnowledgeFile(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return File{}, ErrNotFound
	}
	if err != nil {
		return File{}, fmt.Errorf("get knowledge file: %w", err)
	}
	return fileFromGetRow(row), nil
}

// Delete removes an immutable file and its chunks through the database cascade.
// Any queued or running parse job is cancelled in the same transaction so
// deleting an upload also releases parser capacity.
func (s *Service) Delete(ctx context.Context, id string) (File, error) {
	if s == nil || s.db == nil || s.q == nil || s.river == nil {
		return File{}, ErrServiceUnavailable
	}
	jobIDs, err := s.activeParseJobIDs(ctx, id)
	if err != nil {
		return File{}, err
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return File{}, fmt.Errorf("begin knowledge file deletion: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	row, err := s.q.WithTx(tx).DeleteKnowledgeFile(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return File{}, ErrNotFound
	}
	if err != nil {
		return File{}, fmt.Errorf("delete knowledge file: %w", err)
	}
	for _, jobID := range jobIDs {
		if _, err := s.river.JobCancelTx(ctx, tx, jobID); err != nil && !errors.Is(err, river.ErrNotFound) {
			return File{}, fmt.Errorf("cancel knowledge parse job %d: %w", jobID, err)
		}
	}
	if err := commitKnowledgeTransaction(ctx, tx); err != nil {
		return File{}, err
	}
	return fileFromDeleteRow(row), nil
}

// List returns one owner tuple page and quota for its complete quota pool.
func (s *Service) List(ctx context.Context, input ListInput) (ListResult, error) {
	if s == nil || s.q == nil {
		return ListResult{}, ErrServiceUnavailable
	}
	if err := input.Owner.Validate(); err != nil {
		return ListResult{}, err
	}
	pageSize := input.PageSize
	if pageSize == 0 {
		pageSize = defaultListPageSize
	}
	if pageSize < 1 || pageSize > maxListPageSize {
		return ListResult{}, fmt.Errorf("page size must be between 1 and %d", maxListPageSize)
	}
	query := normalizeQuery(input.Query)
	if utf8.RuneCountInString(query) > 200 {
		return ListResult{}, fmt.Errorf("file name query must not exceed 200 characters")
	}

	params := sqlc.ListKnowledgeFilesParams{
		Scope:    string(input.Owner.Scope),
		UserID:   nullableText(input.Owner.UserID),
		AgentID:  nullableText(input.Owner.AgentID),
		Q:        query,
		PageSize: int32(pageSize + 1),
	}
	if input.Cursor != nil {
		if input.Cursor.ID == "" || input.Cursor.CreatedAt.IsZero() {
			return ListResult{}, fmt.Errorf("invalid knowledge list cursor")
		}
		params.CursorCreatedAt = pgtype.Timestamptz{Time: input.Cursor.CreatedAt, Valid: true}
		params.CursorID = nullableText(input.Cursor.ID)
	}

	rows, err := s.q.ListKnowledgeFiles(ctx, params)
	if err != nil {
		return ListResult{}, fmt.Errorf("list knowledge files: %w", err)
	}
	hasMore := len(rows) > pageSize
	if hasMore {
		rows = rows[:pageSize]
	}
	files := make([]File, 0, len(rows))
	for _, row := range rows {
		files = append(files, fileFromListRow(row))
	}
	quota, err := quotaForOwner(ctx, s.q, input.Owner)
	if err != nil {
		return ListResult{}, err
	}
	return ListResult{Files: files, HasMore: hasMore, Quota: quota}, nil
}

// Quota returns the complete pool usage for an owner tuple.
func (s *Service) Quota(ctx context.Context, owner Owner) (Quota, error) {
	if s == nil || s.q == nil {
		return Quota{}, ErrServiceUnavailable
	}
	if err := owner.Validate(); err != nil {
		return Quota{}, err
	}
	return quotaForOwner(ctx, s.q, owner)
}

// Search performs one BM25 query over the four visible scope layers.
func (s *Service) Search(ctx context.Context, userID, agentID, query string, limit int) ([]SearchResult, error) {
	if s == nil || s.q == nil {
		return nil, ErrServiceUnavailable
	}
	if userID == "" || agentID == "" {
		return []SearchResult{}, nil
	}
	query = normalizeSearchQuery(query)
	if query == "" {
		return []SearchResult{}, nil
	}
	queryRunes := utf8.RuneCountInString(query)
	if queryRunes > MaxSearchQueryRunes {
		return nil, fmt.Errorf(
			"knowledge search query must be at most %d Unicode characters",
			MaxSearchQueryRunes,
		)
	}
	startedAt := time.Now()
	if limit == 0 {
		limit = 5
	}
	if limit < 1 || limit > 10 {
		return nil, fmt.Errorf("knowledge search limit must be between 1 and 10")
	}
	rows, err := s.q.SearchKnowledgeChunks(ctx, sqlc.SearchKnowledgeChunksParams{
		Match:   query,
		AgentID: nullableText(agentID),
		UserID:  nullableText(userID),
		Limit:   int32(limit),
	})
	if err != nil {
		s.logger.Warn(
			"knowledge search failed",
			"query_runes", queryRunes,
			"limit", limit,
			"duration_ms", time.Since(startedAt).Milliseconds(),
			"error", err,
		)
		return nil, fmt.Errorf("search knowledge chunks: %w", err)
	}
	results := make([]SearchResult, 0, len(rows))
	hits := make([]string, 0, len(rows))
	for _, row := range rows {
		var locator ChunkLocator
		if err := json.Unmarshal(row.Locator, &locator); err != nil {
			return nil, fmt.Errorf("decode knowledge chunk locator: %w", err)
		}
		results = append(results, SearchResult{
			Content:  row.Content,
			FileName: row.FileName,
			Locator:  publicLocator(locator),
		})
		hits = append(hits, fmt.Sprintf("%s:%s", row.FileID, row.ID))
	}
	s.logger.Info(
		"knowledge search completed",
		"query_runes", queryRunes,
		"limit", limit,
		"result_count", len(results),
		"duration_ms", time.Since(startedAt).Milliseconds(),
		"ranked_file_chunk_ids", hits,
	)
	return results, nil
}

func publicLocator(locator ChunkLocator) *PublicLocator {
	heading := strings.Join(locator.HeadingPath, " > ")
	if locator.FirstPage == nil && locator.LastPage == nil && heading == "" {
		return nil
	}
	return &PublicLocator{
		FirstPage:      locator.FirstPage,
		LastPage:       locator.LastPage,
		HeadingContext: heading,
	}
}

func normalizeSearchQuery(value string) string {
	value = strings.TrimSpace(value)
	var builder strings.Builder
	spacePending := false
	for _, r := range value {
		if r == 0 || r == '\uFFFD' {
			continue
		}
		if r == '\r' || r == '\n' || r == '\t' || r == ' ' {
			spacePending = builder.Len() > 0
			continue
		}
		if spacePending {
			builder.WriteByte(' ')
			spacePending = false
		}
		builder.WriteRune(r)
	}
	normalized := strings.TrimSpace(builder.String())
	if !hasEffectiveText(normalized) {
		return ""
	}
	return normalized
}

type knowledgeQuerier interface {
	GetSystemKnowledgeQuotaUsage(context.Context) (sqlc.GetSystemKnowledgeQuotaUsageRow, error)
	GetSystemAgentKnowledgeQuotaUsage(context.Context, pgtype.Text) (sqlc.GetSystemAgentKnowledgeQuotaUsageRow, error)
	GetPersonalKnowledgeQuotaUsage(context.Context, pgtype.Text) (sqlc.GetPersonalKnowledgeQuotaUsageRow, error)
}

func quotaForOwner(ctx context.Context, q knowledgeQuerier, owner Owner) (Quota, error) {
	switch owner.Scope {
	case ScopeSystem:
		usage, err := q.GetSystemKnowledgeQuotaUsage(ctx)
		if err != nil {
			return Quota{}, fmt.Errorf("load system knowledge quota: %w", err)
		}
		return Quota{
			UsedFiles: usage.UsedFiles,
			MaxFiles:  SystemMaxFiles,
			UsedBytes: usage.UsedBytes,
			MaxBytes:  SystemMaxBytes,
		}, nil
	case ScopeSystemAgent:
		usage, err := q.GetSystemAgentKnowledgeQuotaUsage(ctx, nullableText(owner.AgentID))
		if err != nil {
			return Quota{}, fmt.Errorf("load Agent knowledge quota: %w", err)
		}
		return Quota{
			UsedFiles: usage.UsedFiles,
			MaxFiles:  SystemAgentMaxFiles,
			UsedBytes: usage.UsedBytes,
			MaxBytes:  SystemAgentMaxBytes,
		}, nil
	case ScopeUser, ScopeUserAgent:
		usage, err := q.GetPersonalKnowledgeQuotaUsage(ctx, nullableText(owner.UserID))
		if err != nil {
			return Quota{}, fmt.Errorf("load personal knowledge quota: %w", err)
		}
		return Quota{
			UsedFiles: usage.UsedFiles,
			MaxFiles:  PersonalMaxFiles,
			UsedBytes: usage.UsedBytes,
			MaxBytes:  PersonalMaxBytes,
		}, nil
	default:
		return Quota{}, ErrInvalidOwner
	}
}

func quotaLockKey(owner Owner) int64 {
	var pool string
	switch owner.Scope {
	case ScopeSystem:
		pool = "system"
	case ScopeSystemAgent:
		pool = "system_agent:" + owner.AgentID
	case ScopeUser, ScopeUserAgent:
		pool = "personal:" + owner.UserID
	default:
		pool = "invalid"
	}
	hash := fnv.New64a()
	_, _ = hash.Write([]byte("stella:knowledge:quota:" + pool))
	return int64(hash.Sum64())
}

func nullableText(value string) pgtype.Text {
	return pgtype.Text{String: value, Valid: value != ""}
}

type fileFields struct {
	ID           string
	Scope        string
	UserID       pgtype.Text
	AgentID      pgtype.Text
	FileName     string
	MediaType    string
	SizeBytes    int64
	Status       string
	ErrorMessage pgtype.Text
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

func fileFromFields(row fileFields) File {
	return File{
		ID: row.ID,
		Owner: Owner{
			Scope:   Scope(row.Scope),
			UserID:  row.UserID.String,
			AgentID: row.AgentID.String,
		},
		FileName:     row.FileName,
		MediaType:    row.MediaType,
		SizeBytes:    row.SizeBytes,
		Status:       FileStatus(row.Status),
		ErrorMessage: row.ErrorMessage.String,
		CreatedAt:    row.CreatedAt,
		UpdatedAt:    row.UpdatedAt,
	}
}

func fileFromCreateRow(row sqlc.CreateKnowledgeFileRow) File {
	return fileFromFields(fileFields{
		ID: row.ID, Scope: row.Scope, UserID: row.UserID, AgentID: row.AgentID,
		FileName: row.FileName, MediaType: row.MediaType, SizeBytes: row.SizeBytes,
		Status: row.Status, ErrorMessage: row.ErrorMessage, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	})
}

func fileFromGetRow(row sqlc.GetKnowledgeFileRow) File {
	return fileFromFields(fileFields{
		ID: row.ID, Scope: row.Scope, UserID: row.UserID, AgentID: row.AgentID,
		FileName: row.FileName, MediaType: row.MediaType, SizeBytes: row.SizeBytes,
		Status: row.Status, ErrorMessage: row.ErrorMessage, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	})
}

func fileFromDeleteRow(row sqlc.DeleteKnowledgeFileRow) File {
	return fileFromFields(fileFields{
		ID: row.ID, Scope: row.Scope, UserID: row.UserID, AgentID: row.AgentID,
		FileName: row.FileName, MediaType: row.MediaType, SizeBytes: row.SizeBytes,
		Status: row.Status, ErrorMessage: row.ErrorMessage, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	})
}

func fileFromListRow(row sqlc.ListKnowledgeFilesRow) File {
	return fileFromFields(fileFields{
		ID: row.ID, Scope: row.Scope, UserID: row.UserID, AgentID: row.AgentID,
		FileName: row.FileName, MediaType: row.MediaType, SizeBytes: row.SizeBytes,
		Status: row.Status, ErrorMessage: row.ErrorMessage, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	})
}
