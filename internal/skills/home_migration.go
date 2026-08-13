package skills

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/home"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

const (
	skillHomeMigrationID               = "postgres-to-posix-v1"
	skillHomeMigrationReconcileTimeout = 5 * time.Second
	maxSkillHomeMigrationEvidenceBytes = 16 << 20
)

var (
	ErrSkillHomeMigrationRequired = errors.New("Skill PostgreSQL-to-Home migration is required")
	ErrMarkerOutcomeUnknown       = errors.New("Skill migration marker outcome is unknown")
)

var emptySkillInventoryDigest = func() string {
	h := sha256.New()
	_, _ = h.Write([]byte("stella-skill-home-migration-inventory-v1\x00"))
	return hex.EncodeToString(h.Sum(nil))
}()

type SkillHomeMigrationOptions struct {
	Apply                 bool
	ConfirmWritersStopped bool
	ConfirmBackupVerified bool
}

type SkillHomeMigrationResult struct {
	State           string `json:"state"`
	DryRun          bool   `json:"dry_run"`
	SkillCount      int64  `json:"skill_count"`
	FileCount       int64  `json:"file_count"`
	ContentBytes    int64  `json:"content_bytes"`
	InventoryDigest string `json:"inventory_digest"`
}

type skillMigrationSource struct {
	identity                    Skill
	files                       []revisionFile
	sourceDigest, contentDigest string
	contentBytes                int64
}

type skillMigrationEvidenceItem struct {
	SkillID       string `json:"skill_id"`
	Scope         string `json:"scope"`
	UserID        string `json:"user_id,omitempty"`
	AgentID       string `json:"agent_id,omitempty"`
	Name          string `json:"name"`
	SourceDigest  string `json:"source_digest"`
	ContentDigest string `json:"content_digest"`
	FileCount     int64  `json:"file_count"`
	ContentBytes  int64  `json:"content_bytes"`
}

type SkillHomeMigrator struct {
	db     *pgxpool.Pool
	q      *sqlc.Queries
	store  *POSIXStore
	now    func() time.Time
	commit func(context.Context, pgx.Tx) error
}

func NewSkillHomeMigrator(db *pgxpool.Pool, roots home.RootOpener) (*SkillHomeMigrator, error) {
	store, err := NewPOSIXStore(db, roots)
	if err != nil {
		return nil, err
	}
	return &SkillHomeMigrator{
		db: db, q: sqlc.New(db), store: store,
		now:    func() time.Time { return time.Now().UTC() },
		commit: func(ctx context.Context, tx pgx.Tx) error { return tx.Commit(ctx) },
	}, nil
}

func freshSkillHomeMigrationReconcileContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), skillHomeMigrationReconcileTimeout)
}

func hashMigrationField(h io.Writer, value []byte) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	_, _ = h.Write(size[:])
	_, _ = h.Write(value)
}

func (m *SkillHomeMigrator) inventory(ctx context.Context) ([]skillMigrationSource, SkillHomeMigrationResult, error) {
	tx, err := m.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, SkillHomeMigrationResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	sources, result, err := readSkillMigrationInventory(ctx, m.q.WithTx(tx))
	if err != nil {
		return nil, result, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, result, fmt.Errorf("finish stable Skill inventory: %w", err)
	}
	return sources, result, nil
}

func readSkillMigrationInventory(ctx context.Context, q *sqlc.Queries) ([]skillMigrationSource, SkillHomeMigrationResult, error) {
	rows, err := q.ListSkillHomeMigrationSource(ctx)
	if err != nil {
		return nil, SkillHomeMigrationResult{}, err
	}
	if len(rows) > MaxManagedSkillCatalogEntries {
		return nil, SkillHomeMigrationResult{}, ErrSkillCatalogLimit
	}
	h := sha256.New()
	_, _ = h.Write([]byte("stella-skill-home-migration-inventory-v1\x00"))
	result := SkillHomeMigrationResult{SkillCount: int64(len(rows))}
	sources := make([]skillMigrationSource, 0, len(rows))
	for _, row := range rows {
		identity := identityFromRow(row)
		desired := Skill{
			ID: row.ID, Scope: row.Scope, UserID: identity.UserID, AgentID: identity.AgentID,
			Name: row.Name, Description: row.Description, Status: row.Status,
			DisableModelInvocation: row.DisableModelInvocation, Metadata: row.Metadata,
			CreatedAt: row.CreatedAt.UTC(), UpdatedAt: row.UpdatedAt.UTC(), Version: row.Version,
		}
		manifest, err := canonicalManifest(desired)
		if err != nil {
			return nil, result, fmt.Errorf("Skill %s manifest: %w", row.ID, err)
		}
		stats, err := q.GetSkillHomeMigrationSourceFileStats(ctx, row.ID)
		if err != nil {
			return nil, result, err
		}
		if stats.FileCount <= 0 || stats.FileCount > MaxManagedSkillFiles || stats.ContentBytes > MaxManagedSkillAggregateBytes || stats.MaxContentBytes > MaxManagedSkillFileBytes || stats.MaxPathBytes > MaxManagedSkillPathBytes {
			return nil, result, fmt.Errorf("Skill %s files: %w", row.ID, ErrSkillLimit)
		}
		fileRows, err := q.ListSkillHomeMigrationSourceFile(ctx, row.ID)
		if err != nil {
			return nil, result, err
		}
		files := make([]revisionFile, 0, len(fileRows))
		sourceHash := sha256.New()
		_, _ = sourceHash.Write([]byte("stella-skill-home-migration-source-v1\x00"))
		hashMigrationField(sourceHash, manifest)
		var contentBytes int64
		for _, file := range fileRows {
			files = append(files, revisionFile{Path: file.Path, Mode: 0o644, Content: append([]byte(nil), file.Content...)})
			hashMigrationField(sourceHash, []byte(file.Path))
			hashMigrationField(sourceHash, file.Content)
			contentBytes += int64(len(file.Content))
		}
		files, err = validateRevisionFiles(files)
		if err != nil || int64(len(files)) != stats.FileCount || contentBytes != stats.ContentBytes {
			return nil, result, fmt.Errorf("Skill %s bounded files: %w", row.ID, errors.Join(err, ErrSkillLimit))
		}
		contentDigest, err := digestRevision(manifest, files)
		if err != nil {
			return nil, result, err
		}
		sourceDigest := hex.EncodeToString(sourceHash.Sum(nil))
		hashMigrationField(h, []byte(row.ID))
		hashMigrationField(h, []byte(sourceDigest))
		sources = append(sources, skillMigrationSource{identity: desired, files: files, sourceDigest: sourceDigest, contentDigest: contentDigest, contentBytes: contentBytes})
		result.FileCount += int64(len(files))
		result.ContentBytes += contentBytes
	}
	result.InventoryDigest = hex.EncodeToString(h.Sum(nil))
	return sources, result, nil
}

func sameSkillMigrationInventory(left, right SkillHomeMigrationResult) bool {
	return left.SkillCount == right.SkillCount && left.FileCount == right.FileCount && left.ContentBytes == right.ContentBytes && left.InventoryDigest == right.InventoryDigest
}

func skillMigrationEvidence(sources []skillMigrationSource) ([]skillMigrationEvidenceItem, json.RawMessage, error) {
	items := make([]skillMigrationEvidenceItem, len(sources))
	for i, source := range sources {
		items[i] = skillMigrationEvidenceItem{
			SkillID: source.identity.ID, Scope: source.identity.Scope, UserID: source.identity.UserID,
			AgentID: source.identity.AgentID, Name: source.identity.Name, SourceDigest: source.sourceDigest,
			ContentDigest: source.contentDigest, FileCount: int64(len(source.files)), ContentBytes: source.contentBytes,
		}
	}
	raw, err := json.Marshal(items)
	if err != nil || len(raw) > maxSkillHomeMigrationEvidenceBytes {
		return nil, nil, errors.Join(err, ErrSkillCatalogLimit)
	}
	return items, raw, nil
}

func (m *SkillHomeMigrator) preflight(ctx context.Context, sources []skillMigrationSource) error {
	for _, source := range sources {
		root, err := m.store.openExistingSkillRoot(ctx, source.identity)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("preflight Skill %s: %w", source.identity.ID, err)
		}
		err = preflightSkillMigrationTarget(ctx, root, source)
		closeErr := root.Close()
		if err != nil || closeErr != nil {
			return fmt.Errorf("preflight Skill %s: %w", source.identity.ID, errors.Join(err, closeErr))
		}
	}
	return nil
}

func preflightSkillMigrationTarget(ctx context.Context, root home.SkillRootOperations, source skillMigrationSource) error {
	revision, err := selectedRevisionPath(source.identity, source.contentDigest)
	if err != nil {
		return err
	}
	if _, err := root.Lstat(ctx, revision); err == nil {
		snapshot, verifyErr := readRevisionSnapshot(ctx, root, source.identity, source.contentDigest)
		if verifyErr != nil || snapshot.Skill.ContentDigest != source.contentDigest {
			return errors.Join(ErrSkillDigestConflict, verifyErr)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	current, err := readCurrentSnapshot(ctx, root, source.identity)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil || current.Skill.ContentDigest != source.contentDigest {
		return errors.Join(ErrSkillDigestConflict, err)
	}
	return nil
}

func (m *SkillHomeMigrator) publishSource(ctx context.Context, source skillMigrationSource) error {
	root, err := m.store.openSkillRoot(ctx, source.identity, home.RootReadWrite)
	if err != nil {
		return err
	}
	current, readErr := readCurrentSnapshot(ctx, root, source.identity)
	if readErr == nil {
		if current.Skill.ContentDigest != source.contentDigest {
			_ = root.Close()
			return ErrSkillDigestConflict
		}
		parent := path.Join(managedRevisionRoot, source.identity.ID)
		for _, directory := range []string{parent, managedRevisionRoot, "."} {
			if err := root.SyncDirectory(ctx, directory); err != nil {
				_ = root.Close()
				return fmt.Errorf("fence existing Skill publication: %w", err)
			}
		}
		verified, verifyErr := readCurrentSnapshot(ctx, root, source.identity)
		closeErr := root.Close()
		if verifyErr != nil || closeErr != nil || verified.Skill.ContentDigest != source.contentDigest {
			return errors.Join(verifyErr, closeErr, ErrSkillDigestConflict)
		}
		return nil
	}
	closeErr := root.Close()
	if !errors.Is(readErr, fs.ErrNotExist) || closeErr != nil {
		return errors.Join(readErr, closeErr)
	}
	published, err := m.store.publish(ctx, source.identity, source.files, "", true)
	if err != nil || published.Skill.ContentDigest != source.contentDigest {
		return errors.Join(err, ErrSkillDigestConflict)
	}
	return nil
}

func (m *SkillHomeMigrator) verifyPublished(ctx context.Context, sources []skillMigrationSource) error {
	for _, source := range sources {
		snapshot, err := m.store.loadIdentityRevision(ctx, source.identity, source.contentDigest)
		if err != nil || snapshot.Skill.ContentDigest != source.contentDigest {
			return fmt.Errorf("verify published Skill %s: %w", source.identity.ID, errors.Join(err, ErrSkillDigestConflict))
		}
		current, err := m.store.loadIdentity(ctx, source.identity)
		if err != nil || current.Skill.ContentDigest != source.contentDigest {
			return fmt.Errorf("verify current Skill %s: %w", source.identity.ID, errors.Join(err, ErrSkillDigestConflict))
		}
	}
	return nil
}

func (m *SkillHomeMigrator) Migrate(ctx context.Context, opts SkillHomeMigrationOptions) (result SkillHomeMigrationResult, resultErr error) {
	if opts.Apply && (!opts.ConfirmWritersStopped || !opts.ConfirmBackupVerified) {
		return result, errors.New("migrate-skills: --confirm-writers-stopped and --confirm-backup-verified are required")
	}
	if opts.Apply {
		release, err := m.store.lockManagedMutations(ctx)
		if err != nil {
			return result, err
		}
		defer finishManagedMutation(release, &resultErr)
		if completed, done, err := m.completedResult(ctx, opts); err != nil || done {
			return completed, err
		}
	} else if completed, done, err := m.completedResult(ctx, opts); err != nil || done {
		return completed, err
	}

	_, first, err := m.inventory(ctx)
	if err != nil {
		return first, err
	}
	secondSources, second, err := m.inventory(ctx)
	if err != nil || !sameSkillMigrationInventory(first, second) {
		return second, errors.Join(err, errors.New("migrate-skills: PostgreSQL source changed between stable inventory reads"))
	}
	result = second
	result.State, result.DryRun = "planned", !opts.Apply
	if err := m.preflight(ctx, secondSources); err != nil {
		return result, err
	}
	if !opts.Apply {
		return result, nil
	}
	for _, source := range secondSources {
		if err := m.publishSource(ctx, source); err != nil {
			return result, fmt.Errorf("publish Skill %s: %w", source.identity.ID, err)
		}
	}
	if err := m.verifyPublished(ctx, secondSources); err != nil {
		return result, err
	}
	if err := m.finalize(ctx, secondSources, result); err != nil {
		return result, err
	}
	result.State, result.DryRun = "completed", false
	return result, nil
}

func (m *SkillHomeMigrator) completedResult(ctx context.Context, opts SkillHomeMigrationOptions) (SkillHomeMigrationResult, bool, error) {
	record, err := m.q.GetSkillHomeMigration(ctx, skillHomeMigrationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return SkillHomeMigrationResult{}, false, nil
	}
	if err != nil {
		return SkillHomeMigrationResult{}, false, err
	}
	if err := m.verifyCompletedAuthority(ctx, record); err != nil {
		return SkillHomeMigrationResult{}, true, err
	}
	return resultFromSkillMigration(record, !opts.Apply), true, nil
}

func resultFromSkillMigration(record sqlc.SkillHomeMigration, dryRun bool) SkillHomeMigrationResult {
	return SkillHomeMigrationResult{
		State: record.State, DryRun: dryRun, SkillCount: record.SourceSkillCount,
		FileCount: record.SourceFileCount, ContentBytes: record.SourceContentBytes,
		InventoryDigest: record.SourceInventoryDigest,
	}
}

func (m *SkillHomeMigrator) finalize(ctx context.Context, sources []skillMigrationSource, expected SkillHomeMigrationResult) error {
	tx, err := m.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "LOCK TABLE skill, skill_file, skill_usage, skill_home_migration IN SHARE ROW EXCLUSIVE MODE"); err != nil {
		return err
	}
	qtx := m.q.WithTx(tx)
	_, stable, err := readSkillMigrationInventory(ctx, qtx)
	if err != nil || !sameSkillMigrationInventory(stable, expected) {
		return errors.Join(err, errors.New("migrate-skills: PostgreSQL source drift detected before scrub"))
	}
	items, inventory, err := skillMigrationEvidence(sources)
	if err != nil {
		return err
	}
	now := m.now().UTC()
	completed, err := qtx.CompleteSkillHomeMigration(ctx, sqlc.CompleteSkillHomeMigrationParams{
		ID: skillHomeMigrationID, SourceSkillCount: expected.SkillCount, SourceFileCount: expected.FileCount,
		SourceContentBytes: expected.ContentBytes, SourceInventoryDigest: expected.InventoryDigest,
		Inventory: inventory, AttestedAt: now, CompletedAt: now,
	})
	if err != nil {
		return err
	}
	if completed.RemovedFileCount != expected.FileCount || completed.RemovedContentBytes != expected.ContentBytes || completed.NormalizedSkillCount != expected.SkillCount || completed.UpdatedUsageCount != completed.ExpectedUsageCount {
		return errors.New("migrate-skills: scrub or normalization totals differ from the stable source inventory")
	}
	if err := m.commit(ctx, tx); err != nil {
		reconcileCtx, cancel := freshSkillHomeMigrationReconcileContext()
		reconcileErr := m.verifyExpectedCompleted(reconcileCtx, expected, items)
		cancel()
		if reconcileErr == nil {
			return nil
		}
		return fmt.Errorf("%w: %w", ErrMarkerOutcomeUnknown, errors.Join(err, reconcileErr))
	}
	return nil
}

func decodeSkillMigrationEvidence(record sqlc.SkillHomeMigration) ([]skillMigrationEvidenceItem, error) {
	if record.ID != skillHomeMigrationID || record.State != "completed" || len(record.Inventory) > maxSkillHomeMigrationEvidenceBytes ||
		record.SourceSkillCount < 0 || record.SourceFileCount < 0 || record.SourceContentBytes < 0 ||
		record.CompletedAt.Before(record.WritersStoppedAttestedAt) || record.CompletedAt.Before(record.BackupVerifiedAttestedAt) {
		return nil, errors.New("migrate-skills: invalid completion evidence")
	}
	decoder := json.NewDecoder(bytes.NewReader(record.Inventory))
	decoder.DisallowUnknownFields()
	var items []skillMigrationEvidenceItem
	if err := decoder.Decode(&items); err != nil {
		return nil, fmt.Errorf("migrate-skills: decode completion evidence: %w", err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, errors.New("migrate-skills: trailing completion evidence")
	}
	if int64(len(items)) != record.SourceSkillCount || len(items) > MaxManagedSkillCatalogEntries {
		return nil, errors.New("migrate-skills: completion evidence count differs")
	}
	h := sha256.New()
	_, _ = h.Write([]byte("stella-skill-home-migration-inventory-v1\x00"))
	seen := make(map[string]struct{}, len(items))
	var fileCount, contentBytes int64
	for _, item := range items {
		if !validSkillMigrationEvidenceItem(item) {
			return nil, errors.New("migrate-skills: invalid completion item")
		}
		if _, duplicate := seen[item.SkillID]; duplicate {
			return nil, errors.New("migrate-skills: duplicate completion item")
		}
		seen[item.SkillID] = struct{}{}
		fileCount += item.FileCount
		contentBytes += item.ContentBytes
		hashMigrationField(h, []byte(item.SkillID))
		hashMigrationField(h, []byte(item.SourceDigest))
	}
	if fileCount != record.SourceFileCount || contentBytes != record.SourceContentBytes || hex.EncodeToString(h.Sum(nil)) != record.SourceInventoryDigest {
		return nil, errors.New("migrate-skills: completion digest or totals differ")
	}
	return items, nil
}

func validSkillMigrationEvidenceItem(item skillMigrationEvidenceItem) bool {
	if !validInventoryComponent(item.SkillID) || item.Name == "" || !utf8.ValidString(item.Name) || len(item.Name) > MaxManagedSkillManifestBytes || !validSkillDigest(item.SourceDigest) || !validSkillDigest(item.ContentDigest) || item.FileCount <= 0 || item.FileCount > MaxManagedSkillFiles || item.ContentBytes < 0 || item.ContentBytes > MaxManagedSkillAggregateBytes {
		return false
	}
	switch item.Scope {
	case "system":
		return item.UserID == "" && item.AgentID == ""
	case "system_agent":
		return item.UserID == "" && validInventoryComponent(item.AgentID)
	case "user":
		return validInventoryComponent(item.UserID) && item.AgentID == ""
	case "user_agent":
		return validInventoryComponent(item.UserID) && validInventoryComponent(item.AgentID)
	default:
		return false
	}
}

func (m *SkillHomeMigrator) ownerLive(ctx context.Context, item skillMigrationEvidenceItem) (bool, error) {
	if item.UserID != "" {
		if _, err := m.q.GetAuthUser(ctx, item.UserID); errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		} else if err != nil {
			return false, err
		}
	}
	if item.AgentID != "" {
		if _, err := m.q.GetAgent(ctx, item.AgentID); errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		} else if err != nil {
			return false, err
		}
	}
	return true, nil
}

func (m *SkillHomeMigrator) verifyCompletedAuthority(ctx context.Context, record sqlc.SkillHomeMigration) error {
	items, err := decodeSkillMigrationEvidence(record)
	if err != nil {
		return err
	}
	readiness, err := m.q.GetSkillHomeMigrationReadiness(ctx)
	if err != nil || readiness.LegacyFileCount != 0 || readiness.UnnormalizedSkillCount != 0 || readiness.MissingUsageDigestCount != 0 {
		return errors.Join(err, ErrSkillHomeMigrationRequired)
	}
	for _, item := range items {
		live, err := m.ownerLive(ctx, item)
		if err != nil {
			return err
		}
		if !live {
			continue
		}
		identity := Skill{ID: item.SkillID, Scope: item.Scope, UserID: item.UserID, AgentID: item.AgentID, Name: item.Name}
		snapshot, err := m.store.loadIdentityRevision(ctx, identity, item.ContentDigest)
		if err != nil || snapshot.Skill.ContentDigest != item.ContentDigest {
			return fmt.Errorf("verify migrated Skill %s: %w", item.SkillID, errors.Join(err, ErrSkillDigestConflict))
		}
		currentIdentity, err := m.store.GetIdentity(ctx, item.SkillID)
		if err != nil {
			return err
		}
		if currentIdentity != nil {
			if !sameSkillIdentity(*currentIdentity, identity) {
				return fmt.Errorf("verify current Skill %s: %w", item.SkillID, ErrInvalidSkillRevision)
			}
			if _, err := m.store.loadIdentity(ctx, *currentIdentity); err != nil {
				return fmt.Errorf("verify current Skill %s: %w", item.SkillID, err)
			}
		}
	}
	return nil
}

func (m *SkillHomeMigrator) verifyCompleted(ctx context.Context) error {
	record, err := m.q.GetSkillHomeMigration(ctx, skillHomeMigrationID)
	if err != nil {
		return err
	}
	return m.verifyCompletedAuthority(ctx, record)
}

func (m *SkillHomeMigrator) verifyExpectedCompleted(ctx context.Context, expected SkillHomeMigrationResult, expectedItems []skillMigrationEvidenceItem) error {
	record, err := m.q.GetSkillHomeMigration(ctx, skillHomeMigrationID)
	if err != nil {
		return err
	}
	items, err := decodeSkillMigrationEvidence(record)
	if err != nil {
		return err
	}
	if record.SourceSkillCount != expected.SkillCount || record.SourceFileCount != expected.FileCount ||
		record.SourceContentBytes != expected.ContentBytes || record.SourceInventoryDigest != expected.InventoryDigest ||
		!slices.Equal(items, expectedItems) {
		return errors.New("migrate-skills: completed marker differs from attempted inventory")
	}
	return m.verifyCompletedAuthority(ctx, record)
}

// EnsureSkillHomeMigrationReady gates runtime startup on the one-way cutover.
// A genuinely empty fresh database receives an empty completion marker; any GA
// Skill rows require the explicit offline operator command.
func (m *SkillHomeMigrator) EnsureReady(ctx context.Context) error {
	if err := m.verifyCompleted(ctx); err == nil {
		return nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: %w", ErrSkillHomeMigrationRequired, err)
	}
	tx, err := m.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "LOCK TABLE skill, skill_file, skill_usage, skill_home_migration IN SHARE ROW EXCLUSIVE MODE"); err != nil {
		return err
	}
	qtx := m.q.WithTx(tx)
	if record, err := qtx.GetSkillHomeMigration(ctx, skillHomeMigrationID); err == nil {
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		return m.verifyCompletedAuthority(ctx, record)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	sources, empty, err := readSkillMigrationInventory(ctx, qtx)
	if err != nil {
		return err
	}
	if len(sources) != 0 || empty.SkillCount != 0 || empty.FileCount != 0 || empty.ContentBytes != 0 || empty.InventoryDigest != emptySkillInventoryDigest {
		return errors.New("Skill storage migration is incomplete; stop Skill writers, verify a backup, then run `stellad storage migrate-skills --apply --confirm-writers-stopped --confirm-backup-verified`")
	}
	now := m.now().UTC()
	completed, err := qtx.CompleteSkillHomeMigration(ctx, sqlc.CompleteSkillHomeMigrationParams{
		ID: skillHomeMigrationID, SourceInventoryDigest: emptySkillInventoryDigest,
		Inventory: json.RawMessage(`[]`), AttestedAt: now, CompletedAt: now,
	})
	if err != nil || completed.RemovedFileCount != 0 || completed.RemovedContentBytes != 0 || completed.NormalizedSkillCount != 0 || completed.UpdatedUsageCount != 0 || completed.ExpectedUsageCount != 0 {
		return errors.Join(err, errors.New("initialize empty Skill migration evidence: unexpected source rows"))
	}
	if err := m.commit(ctx, tx); err != nil {
		reconcileCtx, cancel := freshSkillHomeMigrationReconcileContext()
		reconcileErr := m.verifyExpectedCompleted(reconcileCtx, empty, []skillMigrationEvidenceItem{})
		cancel()
		if reconcileErr == nil {
			return nil
		}
		return fmt.Errorf("%w: %w", ErrMarkerOutcomeUnknown, errors.Join(err, reconcileErr))
	}
	return nil
}
