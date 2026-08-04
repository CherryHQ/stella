package knowledge

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

const (
	rawCompensationTimeout          = 5 * time.Second
	defaultSnapshotCommitTimeout    = 30 * time.Second
	defaultDatabaseStatementTimeout = 20 * time.Second
	defaultDatabaseLockTimeout      = 10 * time.Second
)

// ServiceConfig contains only the storage-core dependencies. The service is
// intentionally not composed into stellad until the chunk worker is delivered.
type ServiceConfig struct {
	DB                       *pgxpool.Pool
	RawStore                 RawStore
	River                    *river.Client[pgx.Tx]
	Logger                   *slog.Logger
	TempDir                  string
	MaxConcurrentUploads     int
	MaxSpoolBytes            int64
	AgentAccess              *agentaccess.Service
	SnapshotCommitTimeout    time.Duration
	DatabaseStatementTimeout time.Duration
	DatabaseLockTimeout      time.Duration
}

// Service owns authorization, bounded acquisition, immutable raw publication,
// and the short metadata-plus-job transaction.
type Service struct {
	db          *pgxpool.Pool
	q           *sqlc.Queries
	rawStore    RawStore
	river       *river.Client[pgx.Tx]
	logger      *slog.Logger
	tempDir     string
	spool       *spoolBudget
	agentAccess *agentaccess.Service

	snapshotCommitTimeout    time.Duration
	databaseStatementTimeout time.Duration
	databaseLockTimeout      time.Duration

	// commitTx is replaceable only by same-package fault tests so they can model
	// a successful commit whose acknowledgement is lost.
	commitTx func(context.Context, pgx.Tx) error
}

func NewService(config ServiceConfig) (*Service, error) {
	if config.DB == nil {
		return nil, fmt.Errorf("knowledge database is required")
	}
	if config.RawStore == nil {
		return nil, fmt.Errorf("knowledge RawStore is required")
	}
	if config.River == nil {
		return nil, fmt.Errorf("knowledge River client is required")
	}
	budget, err := newSpoolBudget(config.MaxConcurrentUploads, config.MaxSpoolBytes)
	if err != nil {
		return nil, err
	}
	logger := config.Logger
	if logger == nil {
		logger = slog.Default().With("component", "knowledge")
	}
	tempDir := config.TempDir
	if tempDir == "" {
		tempDir = os.TempDir()
	}
	snapshotCommitTimeout := defaultDuration(config.SnapshotCommitTimeout, defaultSnapshotCommitTimeout)
	statementTimeout := defaultDuration(config.DatabaseStatementTimeout, defaultDatabaseStatementTimeout)
	lockTimeout := defaultDuration(config.DatabaseLockTimeout, defaultDatabaseLockTimeout)
	if statementTimeout > snapshotCommitTimeout {
		return nil, fmt.Errorf("knowledge database statement timeout must not exceed snapshot commit timeout")
	}
	if lockTimeout > statementTimeout {
		return nil, fmt.Errorf("knowledge database lock timeout must not exceed statement timeout")
	}
	service := &Service{
		db:                       config.DB,
		q:                        sqlc.New(config.DB),
		rawStore:                 config.RawStore,
		river:                    config.River,
		logger:                   logger,
		tempDir:                  tempDir,
		spool:                    budget,
		agentAccess:              config.AgentAccess,
		snapshotCommitTimeout:    snapshotCommitTimeout,
		databaseStatementTimeout: statementTimeout,
		databaseLockTimeout:      lockTimeout,
	}
	service.commitTx = func(ctx context.Context, tx pgx.Tx) error { return tx.Commit(ctx) }
	return service, nil
}

func defaultDuration(value, fallback time.Duration) time.Duration {
	if value <= 0 {
		return fallback
	}
	return value
}

// CreateManagedUpload is the upload acquisition boundary. Scope and Agent
// authorization finish before prepareUpload consumes a single source byte.
func (s *Service) CreateManagedUpload(
	ctx context.Context,
	authority authz.Authority,
	scope Scope,
	agentID string,
	fileName string,
	source io.Reader,
) (File, error) {
	owner, err := s.ResolveManageOwner(ctx, authority, scope, agentID)
	if err != nil {
		return File{}, err
	}
	return s.createSnapshot(ctx, owner, fileName, source)
}

func (s *Service) createSnapshot(
	ctx context.Context,
	owner Owner,
	fileName string,
	source io.Reader,
) (File, error) {
	if s == nil || s.db == nil || s.rawStore == nil || s.river == nil || s.spool == nil {
		return File{}, ErrServiceUnavailable
	}
	if err := owner.Validate(); err != nil {
		return File{}, err
	}
	prepared, err := prepareUpload(ctx, s.tempDir, s.spool, fileName, source)
	if err != nil {
		return File{}, err
	}
	defer prepared.close()

	fileUUID, err := uuid.NewV7()
	if err != nil {
		return File{}, fmt.Errorf("generate knowledge file ID: %w", err)
	}
	fileID := fileUUID.String()
	rawKey, err := RawKey(fileID)
	if err != nil {
		return File{}, err
	}
	// Start the service-owned uncertainty window before raw publication. Once it
	// expires, this uploader can no longer begin or complete the metadata commit.
	snapshotContext, cancelSnapshot := context.WithTimeout(ctx, s.snapshotCommitTimeout)
	defer cancelSnapshot()
	raw, err := os.Open(prepared.path)
	if err != nil {
		return File{}, fmt.Errorf("open prepared knowledge upload: %w", err)
	}
	createErr := s.rawStore.Create(snapshotContext, rawKey, raw)
	closeErr := raw.Close()
	if createErr != nil {
		if errors.Is(createErr, ErrRawAlreadyExists) {
			if verifyErr := s.verifyRawSnapshot(snapshotContext, rawKey, prepared); verifyErr != nil {
				return File{}, verifyErr
			}
			// The upload API never accepts a caller-supplied file ID, so an existing
			// canonical key is a UUID collision, not an idempotent request retry.
			return File{}, ErrRawAlreadyExists
		}
		return File{}, createErr
	}
	if closeErr != nil {
		// The RawStore has already consumed and published the stream. A local
		// reader close error cannot invalidate that canonical snapshot.
		s.logger.Warn("prepared knowledge upload close failed", "file_id", fileID, "error", closeErr)
	}

	file, commitAttempted, err := s.commitSnapshot(snapshotContext, fileID, owner, prepared)
	if err == nil {
		return file, nil
	}
	if !commitAttempted {
		// Before Commit is invoked, the database outcome is known. Compensation
		// may safely remove the raw created by this request.
		cleanupContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), rawCompensationTimeout)
		defer cancel()
		if deleteErr := s.rawStore.Delete(cleanupContext, rawKey); deleteErr != nil {
			s.logger.Warn(
				"knowledge raw compensation failed",
				"file_id", fileID,
				"error", deleteErr,
			)
		}
	}
	// Once Commit was invoked, its error may mean "committed but response lost".
	// Deleting here could destroy an owned snapshot; age-based GC resolves only
	// database-confirmed orphans in the lifecycle slice.
	return File{}, err
}

func (s *Service) commitSnapshot(
	ctx context.Context,
	fileID string,
	owner Owner,
	prepared *preparedUpload,
) (File, bool, error) {
	tx, queries, err := s.beginBoundedTx(ctx)
	if err != nil {
		return File{}, false, fmt.Errorf("begin knowledge snapshot commit: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err := queries.LockKnowledgeQuotaPool(ctx, quotaLockKey(owner)); err != nil {
		return File{}, false, fmt.Errorf("lock knowledge quota: %w", err)
	}
	quota, err := quotaForOwner(ctx, queries, owner)
	if err != nil {
		return File{}, false, err
	}
	if !quota.CanAdd(prepared.sizeBytes) {
		return File{}, false, &QuotaExceededError{Quota: quota}
	}

	row, err := queries.CreateKnowledgeFile(ctx, sqlc.CreateKnowledgeFileParams{
		ID:        fileID,
		Scope:     string(owner.Scope),
		UserID:    nullableText(owner.UserID),
		AgentID:   nullableText(owner.AgentID),
		FileName:  prepared.fileName,
		MediaType: prepared.mediaType,
		SizeBytes: prepared.sizeBytes,
		RawSha256: append([]byte(nil), prepared.rawSHA256...),
	})
	if err != nil {
		return File{}, false, fmt.Errorf("create knowledge file metadata: %w", err)
	}
	args := chunkArgs{FileID: fileID}
	options := args.InsertOpts()
	if _, err := s.river.InsertTx(ctx, tx, args, &options); err != nil {
		return File{}, false, fmt.Errorf("enqueue knowledge chunk job: %w", err)
	}

	commitAttempted := true
	if err := s.commitTx(ctx, tx); err != nil {
		return File{}, commitAttempted, fmt.Errorf("commit knowledge snapshot: %w", err)
	}
	return fileFromCreateRow(row), commitAttempted, nil
}

func (s *Service) beginBoundedTx(ctx context.Context) (pgx.Tx, *sqlc.Queries, error) {
	statementTimeout, lockTimeout, err := s.remainingDatabaseTimeouts(ctx)
	if err != nil {
		return nil, nil, err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	if _, err := tx.Exec(ctx, `
		SELECT
			set_config('statement_timeout', $1, true),
			set_config('lock_timeout', $2, true)
	`, statementTimeout.String(), lockTimeout.String()); err != nil {
		_ = tx.Rollback(context.Background())
		return nil, nil, err
	}
	return tx, s.q.WithTx(tx), nil
}

func (s *Service) remainingDatabaseTimeouts(ctx context.Context) (time.Duration, time.Duration, error) {
	statementTimeout := s.databaseStatementTimeout
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return 0, 0, context.DeadlineExceeded
		}
		if remaining < statementTimeout {
			statementTimeout = remaining
		}
	}
	lockTimeout := min(statementTimeout, s.databaseLockTimeout)
	return statementTimeout, lockTimeout, nil
}

func (s *Service) verifyRawSnapshot(
	ctx context.Context,
	rawKey string,
	prepared *preparedUpload,
) error {
	object, err := s.rawStore.Open(ctx, rawKey)
	if err != nil {
		return fmt.Errorf("open existing knowledge raw object: %w", err)
	}
	defer func() { _ = object.Close() }()
	hash := sha256.New()
	size, err := io.Copy(hash, io.LimitReader(contextReader(ctx, object), MaxFileBytes+1))
	if err != nil {
		return fmt.Errorf("verify existing knowledge raw object: %w", err)
	}
	if size != prepared.sizeBytes || subtle.ConstantTimeCompare(hash.Sum(nil), prepared.rawSHA256) != 1 {
		return fmt.Errorf("%w: existing object differs from prepared snapshot", ErrRawAlreadyExists)
	}
	return nil
}

// Get returns live internal metadata only; raw bytes are never exposed.
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

type knowledgeQuerier interface {
	GetSystemKnowledgeQuotaUsage(context.Context) (sqlc.GetSystemKnowledgeQuotaUsageRow, error)
	GetSystemAgentKnowledgeQuotaUsage(context.Context, pgtype.Text) (sqlc.GetSystemAgentKnowledgeQuotaUsageRow, error)
	GetPersonalKnowledgeQuotaUsage(context.Context, pgtype.Text) (sqlc.GetPersonalKnowledgeQuotaUsageRow, error)
}

func quotaForOwner(ctx context.Context, queries knowledgeQuerier, owner Owner) (Quota, error) {
	switch owner.Scope {
	case ScopeSystem:
		usage, err := queries.GetSystemKnowledgeQuotaUsage(ctx)
		if err != nil {
			return Quota{}, fmt.Errorf("load system knowledge quota: %w", err)
		}
		return Quota{usage.UsedFiles, SystemMaxFiles, usage.UsedBytes, SystemMaxBytes}, nil
	case ScopeSystemAgent:
		usage, err := queries.GetSystemAgentKnowledgeQuotaUsage(ctx, nullableText(owner.AgentID))
		if err != nil {
			return Quota{}, fmt.Errorf("load Agent knowledge quota: %w", err)
		}
		return Quota{usage.UsedFiles, SystemAgentMaxFiles, usage.UsedBytes, SystemAgentMaxBytes}, nil
	case ScopeUser, ScopeUserAgent:
		usage, err := queries.GetPersonalKnowledgeQuotaUsage(ctx, nullableText(owner.UserID))
		if err != nil {
			return Quota{}, fmt.Errorf("load personal knowledge quota: %w", err)
		}
		return Quota{usage.UsedFiles, PersonalMaxFiles, usage.UsedBytes, PersonalMaxBytes}, nil
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
	ID               string
	Scope            string
	UserID           pgtype.Text
	AgentID          pgtype.Text
	FileName         string
	MediaType        string
	SizeBytes        int64
	RawSHA256        []byte
	Status           string
	ErrorMessage     pgtype.Text
	ActiveChunkSetID pgtype.Text
	DeletedAt        pgtype.Timestamptz
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

func fileFromFields(row fileFields) File {
	var deletedAt *time.Time
	if row.DeletedAt.Valid {
		value := row.DeletedAt.Time
		deletedAt = &value
	}
	return File{
		ID: row.ID,
		Owner: Owner{
			Scope: Scope(row.Scope), UserID: row.UserID.String, AgentID: row.AgentID.String,
		},
		FileName:         row.FileName,
		MediaType:        row.MediaType,
		SizeBytes:        row.SizeBytes,
		RawSHA256:        append([]byte(nil), row.RawSHA256...),
		Status:           FileStatus(row.Status),
		ErrorMessage:     row.ErrorMessage.String,
		ActiveChunkSetID: row.ActiveChunkSetID.String,
		DeletedAt:        deletedAt,
		CreatedAt:        row.CreatedAt,
		UpdatedAt:        row.UpdatedAt,
	}
}

func fileFromCreateRow(row sqlc.KnowledgeFile) File {
	return fileFromFields(fileFields{
		ID: row.ID, Scope: row.Scope, UserID: row.UserID, AgentID: row.AgentID,
		FileName: row.FileName, MediaType: row.MediaType, SizeBytes: row.SizeBytes,
		RawSHA256: row.RawSha256, Status: row.Status, ErrorMessage: row.ErrorMessage,
		ActiveChunkSetID: row.ActiveChunkSetID, DeletedAt: row.DeletedAt,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	})
}

func fileFromGetRow(row sqlc.KnowledgeFile) File {
	return fileFromFields(fileFields{
		ID: row.ID, Scope: row.Scope, UserID: row.UserID, AgentID: row.AgentID,
		FileName: row.FileName, MediaType: row.MediaType, SizeBytes: row.SizeBytes,
		RawSHA256: row.RawSha256, Status: row.Status, ErrorMessage: row.ErrorMessage,
		ActiveChunkSetID: row.ActiveChunkSetID, DeletedAt: row.DeletedAt,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	})
}
