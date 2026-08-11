package library

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
	"sync"
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
	defaultReconciliationInterval   = 5 * time.Minute
	defaultStaleDerivationAfter     = 15 * time.Minute
	defaultOrphanMinAge             = 10 * time.Minute
	defaultMaxClockSkew             = time.Minute
	defaultOrphanSafetyMargin       = time.Minute
	defaultLibraryWorkers           = 2
)

// Parser is the bounded document parser used by the asynchronous chunk worker.
type Parser interface {
	// Profile must be a pure, stable lookup: lifecycle code calls it while
	// holding database locks and uses the value as durable generation identity.
	Profile(mediaType string) (string, error)
	Parse(ctx context.Context, path, mediaType string) ([]ParsedChunk, error)
}

// ServiceConfig contains the internal Library ingestion and lifecycle
// dependencies. Public management and retrieval surfaces are composed later.
type ServiceConfig struct {
	DB                       *pgxpool.Pool
	RawStore                 RawStore
	Parser                   Parser
	River                    *river.Client[pgx.Tx]
	Logger                   *slog.Logger
	TempDir                  string
	MaxConcurrentUploads     int
	MaxSpoolBytes            int64
	AgentAccess              *agentaccess.Service
	SnapshotCommitTimeout    time.Duration
	DatabaseStatementTimeout time.Duration
	DatabaseLockTimeout      time.Duration
	ReconciliationInterval   time.Duration
	StaleDerivationAfter     time.Duration
	OrphanMinAge             time.Duration
	MaxClockSkew             time.Duration
	OrphanSafetyMargin       time.Duration
	MaxWorkers               int
}

// Service owns authorization, bounded acquisition, immutable raw publication,
// and the short metadata-plus-job transaction.
type Service struct {
	db          *pgxpool.Pool
	q           *sqlc.Queries
	rawStore    RawStore
	parser      Parser
	logger      *slog.Logger
	tempDir     string
	spool       *spoolBudget
	agentAccess *agentaccess.Service

	snapshotCommitTimeout    time.Duration
	databaseStatementTimeout time.Duration
	databaseLockTimeout      time.Duration
	reconciliationInterval   time.Duration
	staleDerivationAfter     time.Duration
	orphanMinAge             time.Duration
	maxWorkers               int

	// mu guards the one-shot River bind and periodic lifecycle. It is never held
	// while calling River or performing storage/database IO.
	mu      sync.Mutex
	river   *river.Client[pgx.Tx]
	started bool

	// commitTx is replaceable only by same-package fault tests so they can model
	// a successful commit whose acknowledgement is lost.
	commitTx func(context.Context, pgx.Tx) error
}

func NewService(config ServiceConfig) (*Service, error) {
	if config.DB == nil {
		return nil, fmt.Errorf("library database is required")
	}
	if config.RawStore == nil {
		return nil, fmt.Errorf("library RawStore is required")
	}
	if config.Parser == nil {
		return nil, fmt.Errorf("library parser is required")
	}
	budget, err := newSpoolBudget(config.MaxConcurrentUploads, config.MaxSpoolBytes)
	if err != nil {
		return nil, err
	}
	logger := config.Logger
	if logger == nil {
		logger = slog.Default().With("component", "library")
	}
	tempDir := config.TempDir
	if tempDir == "" {
		tempDir = os.TempDir()
	}
	snapshotCommitTimeout := defaultDuration(config.SnapshotCommitTimeout, defaultSnapshotCommitTimeout)
	statementTimeout := defaultDuration(config.DatabaseStatementTimeout, defaultDatabaseStatementTimeout)
	lockTimeout := defaultDuration(config.DatabaseLockTimeout, defaultDatabaseLockTimeout)
	if statementTimeout > snapshotCommitTimeout {
		return nil, fmt.Errorf("library database statement timeout must not exceed snapshot commit timeout")
	}
	if lockTimeout > statementTimeout {
		return nil, fmt.Errorf("library database lock timeout must not exceed statement timeout")
	}
	maxClockSkew := defaultDuration(config.MaxClockSkew, defaultMaxClockSkew)
	orphanSafetyMargin := defaultDuration(config.OrphanSafetyMargin, defaultOrphanSafetyMargin)
	orphanMinAge := defaultDuration(config.OrphanMinAge, defaultOrphanMinAge)
	minimumOrphanAge := snapshotCommitTimeout + maxClockSkew + orphanSafetyMargin
	if orphanMinAge <= minimumOrphanAge {
		return nil, fmt.Errorf(
			"library orphan minimum age %s must be greater than commit window plus skew and safety margin %s",
			orphanMinAge,
			minimumOrphanAge,
		)
	}
	maxWorkers := config.MaxWorkers
	if maxWorkers <= 0 {
		maxWorkers = defaultLibraryWorkers
	}
	service := &Service{
		db:                       config.DB,
		q:                        sqlc.New(config.DB),
		rawStore:                 config.RawStore,
		parser:                   config.Parser,
		river:                    config.River,
		logger:                   logger,
		tempDir:                  tempDir,
		spool:                    budget,
		agentAccess:              config.AgentAccess,
		snapshotCommitTimeout:    snapshotCommitTimeout,
		databaseStatementTimeout: statementTimeout,
		databaseLockTimeout:      lockTimeout,
		reconciliationInterval:   defaultDuration(config.ReconciliationInterval, defaultReconciliationInterval),
		staleDerivationAfter:     defaultDuration(config.StaleDerivationAfter, defaultStaleDerivationAfter),
		orphanMinAge:             orphanMinAge,
		maxWorkers:               maxWorkers,
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

// BindRiverClient injects the single shared working River client. Tests may
// still supply an insert-only client in ServiceConfig; production binds once
// before the client starts.
func (s *Service) BindRiverClient(client *river.Client[pgx.Tx]) error {
	if client == nil {
		return fmt.Errorf("library: BindRiverClient requires a non-nil client")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return fmt.Errorf("library: BindRiverClient after reconciliation start")
	}
	if s.river != nil {
		return fmt.Errorf("library: River client already bound")
	}
	s.river = client
	return nil
}

func (s *Service) riverClient() *river.Client[pgx.Tx] {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.river
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
) (LibraryFile, error) {
	owner, err := s.ResolveManageOwner(ctx, authority, scope, agentID)
	if err != nil {
		return LibraryFile{}, err
	}
	return s.createSnapshot(ctx, owner, fileName, source)
}

func (s *Service) createSnapshot(
	ctx context.Context,
	owner Owner,
	fileName string,
	source io.Reader,
) (LibraryFile, error) {
	if s == nil || s.db == nil || s.rawStore == nil || s.parser == nil || s.spool == nil {
		return LibraryFile{}, ErrServiceUnavailable
	}
	riverClient := s.riverClient()
	if riverClient == nil {
		return LibraryFile{}, ErrServiceUnavailable
	}
	if err := owner.Validate(); err != nil {
		return LibraryFile{}, err
	}
	// Reject an unavailable optional parser before consuming upload bytes. The
	// filename-derived media type is canonical; prepareUpload repeats filename
	// validation at the acquisition boundary before spooling the representation.
	_, mediaType, err := validateUploadName(fileName)
	if err != nil {
		return LibraryFile{}, err
	}
	if _, err := s.parser.Profile(mediaType); err != nil {
		return LibraryFile{}, fmt.Errorf("profile library upload parser: %w", err)
	}
	prepared, err := prepareUpload(ctx, s.tempDir, s.spool, fileName, source)
	if err != nil {
		return LibraryFile{}, err
	}
	defer prepared.close()

	fileUUID, err := uuid.NewV7()
	if err != nil {
		return LibraryFile{}, fmt.Errorf("generate library file ID: %w", err)
	}
	fileID := fileUUID.String()
	rawKey, err := RawKey(fileID)
	if err != nil {
		return LibraryFile{}, err
	}
	// Start the service-owned uncertainty window before raw publication. Once it
	// expires, this uploader can no longer begin or complete the metadata commit.
	snapshotContext, cancelSnapshot := context.WithTimeout(ctx, s.snapshotCommitTimeout)
	defer cancelSnapshot()
	raw, err := os.Open(prepared.path)
	if err != nil {
		return LibraryFile{}, fmt.Errorf("open prepared library upload: %w", err)
	}
	createErr := s.rawStore.Create(snapshotContext, rawKey, raw)
	closeErr := raw.Close()
	if createErr != nil {
		if errors.Is(createErr, ErrRawAlreadyExists) {
			if verifyErr := s.verifyRawSnapshot(snapshotContext, rawKey, prepared); verifyErr != nil {
				return LibraryFile{}, verifyErr
			}
			// The upload API never accepts a caller-supplied file ID, so an existing
			// canonical key is a UUID collision, not an idempotent request retry.
			return LibraryFile{}, ErrRawAlreadyExists
		}
		return LibraryFile{}, createErr
	}
	if closeErr != nil {
		// The RawStore has already consumed and published the stream. A local
		// reader close error cannot invalidate that canonical snapshot.
		s.logger.Warn("prepared library upload close failed", "file_id", fileID, "error", closeErr)
	}

	file, commitAttempted, err := s.commitSnapshot(snapshotContext, riverClient, fileID, owner, prepared)
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
				"library raw compensation failed",
				"file_id", fileID,
				"error", deleteErr,
			)
		}
	}
	// Once Commit was invoked, its error may mean "committed but response lost".
	// Deleting here could destroy an owned snapshot; age-based GC resolves only
	// database-confirmed orphans in the lifecycle slice.
	return LibraryFile{}, err
}

func (s *Service) commitSnapshot(
	ctx context.Context,
	riverClient *river.Client[pgx.Tx],
	fileID string,
	owner Owner,
	prepared *preparedUpload,
) (LibraryFile, bool, error) {
	tx, queries, err := s.beginBoundedTx(ctx)
	if err != nil {
		return LibraryFile{}, false, fmt.Errorf("begin library snapshot commit: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	if err := queries.LockLibraryQuotaPool(ctx, quotaLockKey(owner)); err != nil {
		return LibraryFile{}, false, fmt.Errorf("lock library quota: %w", err)
	}
	quota, err := quotaForOwner(ctx, queries, owner)
	if err != nil {
		return LibraryFile{}, false, err
	}
	if !quota.CanAdd(prepared.sizeBytes) {
		return LibraryFile{}, false, &QuotaExceededError{Quota: quota}
	}

	row, err := queries.CreateLibraryFile(ctx, sqlc.CreateLibraryFileParams{
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
		return LibraryFile{}, false, fmt.Errorf("create library file metadata: %w", err)
	}
	args := chunkArgs{FileID: fileID}
	options := args.InsertOpts()
	if _, err := riverClient.InsertTx(ctx, tx, args, &options); err != nil {
		return LibraryFile{}, false, fmt.Errorf("enqueue library chunk job: %w", err)
	}

	commitAttempted := true
	if err := s.commitTx(ctx, tx); err != nil {
		return LibraryFile{}, commitAttempted, fmt.Errorf("commit library snapshot: %w", err)
	}
	return fileFromCreateRow(row), commitAttempted, nil
}

func (s *Service) beginBoundedTx(ctx context.Context) (pgx.Tx, *sqlc.Queries, error) {
	statementTimeout, lockTimeout, err := s.databaseTimeouts(ctx)
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

func (s *Service) databaseTimeouts(ctx context.Context) (time.Duration, time.Duration, error) {
	if err := ctx.Err(); err != nil {
		return 0, 0, err
	}
	// Keep PostgreSQL's independent limits at their configured values. Clamping
	// them to an earlier request deadline makes the server timeout race the Go
	// context and can leak SQLSTATE 57014 instead of the caller's context error.
	return s.databaseStatementTimeout, s.databaseLockTimeout, nil
}

func (s *Service) verifyRawSnapshot(
	ctx context.Context,
	rawKey string,
	prepared *preparedUpload,
) error {
	object, err := s.rawStore.Open(ctx, rawKey)
	if err != nil {
		return fmt.Errorf("open existing library raw object: %w", err)
	}
	defer func() { _ = object.Close() }()
	hash := sha256.New()
	size, err := io.Copy(hash, io.LimitReader(contextReader(ctx, object), MaxFileBytes+1))
	if err != nil {
		return fmt.Errorf("verify existing library raw object: %w", err)
	}
	if size != prepared.sizeBytes || subtle.ConstantTimeCompare(hash.Sum(nil), prepared.rawSHA256) != 1 {
		return fmt.Errorf("%w: existing object differs from prepared snapshot", ErrRawAlreadyExists)
	}
	return nil
}

// Get returns live internal metadata only; raw bytes are never exposed.
func (s *Service) Get(ctx context.Context, id string) (LibraryFile, error) {
	if s == nil || s.q == nil {
		return LibraryFile{}, ErrServiceUnavailable
	}
	row, err := s.q.GetLibraryFile(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return LibraryFile{}, ErrNotFound
	}
	if err != nil {
		return LibraryFile{}, fmt.Errorf("get library file: %w", err)
	}
	return fileFromGetRow(row), nil
}

type libraryQuerier interface {
	GetSystemLibraryQuotaUsage(context.Context) (sqlc.GetSystemLibraryQuotaUsageRow, error)
	GetSystemAgentLibraryQuotaUsage(context.Context, pgtype.Text) (sqlc.GetSystemAgentLibraryQuotaUsageRow, error)
	GetPersonalLibraryQuotaUsage(context.Context, pgtype.Text) (sqlc.GetPersonalLibraryQuotaUsageRow, error)
}

func quotaForOwner(ctx context.Context, queries libraryQuerier, owner Owner) (Quota, error) {
	switch owner.Scope {
	case ScopeSystem:
		usage, err := queries.GetSystemLibraryQuotaUsage(ctx)
		if err != nil {
			return Quota{}, fmt.Errorf("load system library quota: %w", err)
		}
		return Quota{usage.UsedFiles, SystemMaxFiles, usage.UsedBytes, SystemMaxBytes}, nil
	case ScopeSystemAgent:
		usage, err := queries.GetSystemAgentLibraryQuotaUsage(ctx, nullableText(owner.AgentID))
		if err != nil {
			return Quota{}, fmt.Errorf("load Agent library quota: %w", err)
		}
		return Quota{usage.UsedFiles, SystemAgentMaxFiles, usage.UsedBytes, SystemAgentMaxBytes}, nil
	case ScopeUser, ScopeUserAgent:
		usage, err := queries.GetPersonalLibraryQuotaUsage(ctx, nullableText(owner.UserID))
		if err != nil {
			return Quota{}, fmt.Errorf("load personal library quota: %w", err)
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
	_, _ = hash.Write([]byte("stella:library:quota:" + pool))
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

func fileFromFields(row fileFields) LibraryFile {
	var deletedAt *time.Time
	if row.DeletedAt.Valid {
		value := row.DeletedAt.Time
		deletedAt = &value
	}
	return LibraryFile{
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

func fileFromCreateRow(row sqlc.LibraryFile) LibraryFile {
	return fileFromFields(fileFields{
		ID: row.ID, Scope: row.Scope, UserID: row.UserID, AgentID: row.AgentID,
		FileName: row.FileName, MediaType: row.MediaType, SizeBytes: row.SizeBytes,
		RawSHA256: row.RawSha256, Status: row.Status, ErrorMessage: row.ErrorMessage,
		ActiveChunkSetID: row.ActiveChunkSetID, DeletedAt: row.DeletedAt,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	})
}

func fileFromGetRow(row sqlc.LibraryFile) LibraryFile {
	return fileFromFields(fileFields{
		ID: row.ID, Scope: row.Scope, UserID: row.UserID, AgentID: row.AgentID,
		FileName: row.FileName, MediaType: row.MediaType, SizeBytes: row.SizeBytes,
		RawSHA256: row.RawSha256, Status: row.Status, ErrorMessage: row.ErrorMessage,
		ActiveChunkSetID: row.ActiveChunkSetID, DeletedAt: row.DeletedAt,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	})
}

func fileFromListRow(row sqlc.LibraryFile) LibraryFile {
	return fileFromFields(fileFields{
		ID: row.ID, Scope: row.Scope, UserID: row.UserID, AgentID: row.AgentID,
		FileName: row.FileName, MediaType: row.MediaType, SizeBytes: row.SizeBytes,
		RawSHA256: row.RawSha256, Status: row.Status, ErrorMessage: row.ErrorMessage,
		ActiveChunkSetID: row.ActiveChunkSetID, DeletedAt: row.DeletedAt,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	})
}
