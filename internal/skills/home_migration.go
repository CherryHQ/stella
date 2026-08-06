package skills

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/home"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	"github.com/CherryHQ/stella/pkg/sandbox"
)

// SkillHomeAuthorityMigration is the one offline PG-to-Home cutover marker.
const (
	SkillHomeAuthorityMigration = "skill_home_authority_v1"
	skillHomeAuthorityLayout    = "skill_home_authority_v1"
	skillMigrationArchiveRoot   = ".stella-migration/archive"
	maxMigrationChangelogRows   = 4096
	maxMigrationChangelogBytes  = 16 << 20
	maxMigrationIssues          = 128
	maxMigrationIssueReason     = 256
)

type SkillMigrationOptions struct{ DryRun bool }

// SkillMigrationSummary is stable operator output. Counts are source facts, not
// catalog inventory, so deprecated rows remain visible in the audit result.
type SkillMigrationSummary struct {
	DryRun           bool                  `json:"dry_run"`
	Status           string                `json:"status"`
	MarkerState      string                `json:"marker_state"`
	SourceCount      int64                 `json:"source_count"`
	Files            int64                 `json:"files"`
	Bytes            int64                 `json:"bytes"`
	SHA256           string                `json:"sha256"`
	ActiveCount      int64                 `json:"active_count"`
	ArchiveCount     int64                 `json:"archive_count"`
	UsageCount       int64                 `json:"usage_count"`
	UnsupportedCount int64                 `json:"unsupported_count"`
	ConflictCount    int64                 `json:"conflict_count"`
	Issues           []SkillMigrationIssue `json:"issues,omitempty"`
}

// SkillMigrationIssue is a bounded, operator-safe preflight finding. It never
// contains a Home locator or host path.
type SkillMigrationIssue struct {
	SkillID string `json:"skill_id"`
	Kind    string `json:"kind"`
	Reason  string `json:"reason"`
}

// SkillMigrationBlockedError means preflight found source or target data that
// requires an operator decision. Summary is safe to render to an operator.
type SkillMigrationBlockedError struct{ Summary SkillMigrationSummary }

func (e *SkillMigrationBlockedError) Error() string { return "skills: migration preflight blocked" }

func BlockedSkillMigrationSummary(err error) (SkillMigrationSummary, bool) {
	var blocked *SkillMigrationBlockedError
	if errors.As(err, &blocked) {
		return blocked.Summary, true
	}
	return SkillMigrationSummary{}, false
}

// unsupportedSkillMigrationSourceError marks only deterministic legacy data
// defects. Database, context, and filesystem failures deliberately remain
// operational errors rather than operator reports.
type unsupportedSkillMigrationSourceError struct {
	reason string
	err    error
}

func (e *unsupportedSkillMigrationSourceError) Error() string { return e.reason }
func (e *unsupportedSkillMigrationSourceError) Unwrap() error { return e.err }

func unsupportedSource(reason string, err error) error {
	return &unsupportedSkillMigrationSourceError{reason: reason, err: err}
}

type skillMigrationService struct {
	db    *pgxpool.Pool
	q     *sqlc.Queries
	homes *home.Registry
}

func NewSkillHomeMigrationService(db *pgxpool.Pool, homes *home.Registry) (*skillMigrationService, error) {
	if db == nil || homes == nil {
		return nil, errors.New("skills: database and Home registry are required")
	}
	return &skillMigrationService{db: db, q: sqlc.New(db), homes: homes}, nil
}

// MigrateSkillHomeAuthority is deliberately not wired into server authority.
// It only publishes immutable backups and future Home revisions while PG stays
// untouched except for the additive usage identity and completion marker.
func (s *skillMigrationService) MigrateSkillHomeAuthority(ctx context.Context, options SkillMigrationOptions) (summary SkillMigrationSummary, err error) {
	didPublish := false
	defer func() {
		if didPublish && err != nil && !errors.Is(err, sandbox.ErrOutcomeUnknown) {
			err = fmt.Errorf("%w: %w", sandbox.ErrOutcomeUnknown, err)
		}
	}()
	// Skills are published only after the mutable-asset migration has reached a
	// durable safe gate. This applies to dry-runs too: a plan based on an unsafe
	// predecessor is not useful operator evidence.
	if err := s.validateMutableAssetGate(ctx); err != nil {
		return SkillMigrationSummary{}, err
	}
	marker, err := s.marker(ctx, options.DryRun)
	if err != nil {
		return SkillMigrationSummary{}, err
	}
	rows, err := s.q.ListSkillMigrationSource(ctx)
	if err != nil {
		return SkillMigrationSummary{}, fmt.Errorf("skills: list migration source: %w", err)
	}
	usage, err := s.q.ListSkillUsageForMigration(ctx)
	if err != nil {
		return SkillMigrationSummary{}, fmt.Errorf("skills: list migration usage: %w", err)
	}
	usageBySkill, usageIssues := migrationUsageBySkill(rows, usage)
	summary, records, err := s.scan(ctx, rows, usageBySkill, usageIssues)
	if err != nil {
		return SkillMigrationSummary{}, err
	}
	summary.DryRun, summary.MarkerState = options.DryRun, marker.State
	if marker.State == "completed" {
		if err := validateSkillMigrationMarker(marker, skillMigrationMetadata(summary)); err != nil {
			return SkillMigrationSummary{}, fmt.Errorf("skills: completed Skill migration metadata differs from source: %w", err)
		}
	}
	if summary.UnsupportedCount > 0 {
		summary.Status = "blocked"
		return summary, &SkillMigrationBlockedError{Summary: summary}
	}
	if marker.State == "completed" {
		if err := s.verify(ctx, records); err != nil {
			return SkillMigrationSummary{}, err
		}
		if err := s.verifyUsage(ctx, records); err != nil {
			return SkillMigrationSummary{}, err
		}
		if options.DryRun {
			summary.Status = "planned"
			return summary, nil
		}
		summary.Status, summary.MarkerState = "completed", "completed"
		return summary, nil
	}
	if err := s.preflightTargets(ctx, records, &summary); err != nil {
		return SkillMigrationSummary{}, err
	}
	if summary.ConflictCount > 0 {
		summary.Status = "blocked"
		return summary, &SkillMigrationBlockedError{Summary: summary}
	}
	if options.DryRun {
		summary.Status = "planned"
		return summary, nil
	}
	for _, record := range records {
		published, publishErr := s.publishRecord(ctx, record)
		didPublish = didPublish || published
		if publishErr != nil {
			return SkillMigrationSummary{}, publishErr
		}
		if err := s.updateUsage(ctx, record); err != nil {
			return SkillMigrationSummary{}, err
		}
	}
	freshUsage, usageErr := s.q.ListSkillUsageForMigration(ctx)
	if usageErr != nil {
		return SkillMigrationSummary{}, fmt.Errorf("skills: relist migration usage: %w", usageErr)
	}
	freshRows, listErr := s.q.ListSkillMigrationSource(ctx)
	if listErr != nil {
		return SkillMigrationSummary{}, fmt.Errorf("skills: relist migration source: %w", listErr)
	}
	freshUsageBySkill, freshUsageIssues := migrationUsageBySkill(freshRows, freshUsage)
	freshSummary, freshRecords, err := s.scan(ctx, freshRows, freshUsageBySkill, freshUsageIssues)
	if err != nil {
		return SkillMigrationSummary{}, err
	}
	if freshSummary.UnsupportedCount > 0 {
		return SkillMigrationSummary{}, errors.New("skills: source became unsupported during migration")
	}
	if !sameSkillMigrationAggregate(summary, freshSummary) {
		return SkillMigrationSummary{}, errors.New("skills: source changed during migration")
	}
	if err := s.verify(ctx, freshRecords); err != nil {
		return SkillMigrationSummary{}, err
	}
	if err := s.verifyUsage(ctx, freshRecords); err != nil {
		return SkillMigrationSummary{}, err
	}
	encoded := skillMigrationMetadata(summary)
	completedMarker, err := s.q.CompleteSkillAuthorityStorageMigration(ctx, sqlc.CompleteSkillAuthorityStorageMigrationParams{Name: SkillHomeAuthorityMigration, Metadata: encoded, Metadata_2: []byte(`{}`)})
	if err != nil {
		reloaded, reloadErr := s.q.GetStorageMigration(ctx, SkillHomeAuthorityMigration)
		if reloadErr == nil && validateSkillMigrationMarker(reloaded, encoded) == nil {
			summary.Status, summary.MarkerState = "completed", "completed"
			return summary, nil
		}
		return SkillMigrationSummary{}, fmt.Errorf("%w: complete Skill migration marker: %w", sandbox.ErrOutcomeUnknown, errors.Join(err, reloadErr))
	}
	if err := validateSkillMigrationMarker(completedMarker, encoded); err != nil {
		return SkillMigrationSummary{}, fmt.Errorf("%w: completed Skill migration marker: %w", sandbox.ErrOutcomeUnknown, err)
	}
	summary.Status, summary.MarkerState = "completed", "completed"
	return summary, nil
}

// validateMutableAssetGate is shared by the migration and startup authority
// gate so they accept exactly the same safe predecessor states.
func (s *skillMigrationService) validateMutableAssetGate(ctx context.Context) error {
	assetMarker, err := s.q.GetStorageMigration(ctx, home.MutableAssetObjectAuthorityMigration)
	if err != nil {
		return fmt.Errorf("skills: load mutable asset migration prerequisite: %w", err)
	}
	switch assetMarker.State {
	case "not_required":
		if assetMarker.ObjectAuthorityConfigured {
			return errors.New("skills: malformed mutable asset migration prerequisite")
		}
		if err := s.homes.ValidateMutableAssetMigrationGate(ctx, false); err != nil {
			return fmt.Errorf("skills: validate mutable asset migration prerequisite: %w", err)
		}
	case "completed":
		if !assetMarker.ObjectAuthorityConfigured {
			return errors.New("skills: malformed mutable asset migration prerequisite")
		}
		if err := s.homes.ValidateMutableAssetMigrationGate(ctx, true); err != nil {
			return fmt.Errorf("skills: validate mutable asset migration prerequisite: %w", err)
		}
	default:
		return fmt.Errorf("skills: mutable asset migration prerequisite is not safe (%s)", assetMarker.State)
	}
	return nil
}

type skillMigrationRecord struct {
	row                         sqlc.Skill
	files                       []skillTreeEntry
	activeDigest, archiveDigest string
	archiveName                 string
	changelog                   []sqlc.SkillChangelog
	root                        *home.SkillRoot
	usage                       sqlc.SkillUsage
	hasUsage                    bool
	fileCount, fileBytes        int64
}

func jsonNumber(n int64) json.Number { return json.Number(fmt.Sprintf("%d", n)) }

func (s *skillMigrationService) scan(ctx context.Context, rows []sqlc.Skill, usageBySkill map[string]sqlc.SkillUsage, usageIssues []SkillMigrationIssue) (SkillMigrationSummary, []skillMigrationRecord, error) {
	if rows == nil {
		return SkillMigrationSummary{}, nil, errors.New("skills: list migration source failed")
	}
	out := make([]skillMigrationRecord, 0, len(rows))
	summary := SkillMigrationSummary{}
	for _, issue := range usageIssues {
		summary.addIssue(issue)
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte(skillHomeAuthorityLayout + "\x00"))
	for _, row := range rows {
		summary.SourceCount++
		summary.ArchiveCount++
		if row.Status == SkillStatusActive {
			summary.ActiveCount++
		}
		record, err := s.loadDescriptor(ctx, row)
		if err != nil {
			var unsupported *unsupportedSkillMigrationSourceError
			if !errors.As(err, &unsupported) {
				return SkillMigrationSummary{}, nil, err
			}
			summary.addIssue(SkillMigrationIssue{SkillID: row.ID, Kind: "unsupported", Reason: unsupported.reason})
			continue
		}
		_, _ = hash.Write([]byte(row.ID + "\x00" + record.activeDigest + "\x00" + record.archiveDigest + "\x00"))
		if usage, ok := usageBySkill[row.ID]; ok {
			record.usage, record.hasUsage = usage, true
			canonical, err := canonicalMigrationUsage(usage)
			if err != nil {
				summary.addIssue(SkillMigrationIssue{SkillID: row.ID, Kind: "unsupported", Reason: "usage record is invalid"})
				continue
			}
			_, _ = hash.Write(canonical)
			_, _ = hash.Write([]byte("\x00"))
		}
		if _, ok := usageBySkill[row.ID]; ok {
			summary.UsageCount++
		}
		summary.Files += record.fileCount
		summary.Bytes += record.fileBytes
		out = append(out, record)
	}
	summary.SHA256 = hex.EncodeToString(hash.Sum(nil))
	return summary, out, nil
}

func (s *SkillMigrationSummary) addIssue(issue SkillMigrationIssue) {
	if issue.Kind == "conflict" {
		s.ConflictCount++
	} else {
		s.UnsupportedCount++
	}
	if len(s.Issues) >= maxMigrationIssues {
		return
	}
	issue.SkillID = boundedMigrationIssueText(issue.SkillID)
	issue.Reason = boundedMigrationIssueText(issue.Reason)
	s.Issues = append(s.Issues, issue)
}

func boundedMigrationIssueText(value string) string {
	value = strings.ReplaceAll(value, "\x00", "?")
	if len(value) <= maxMigrationIssueReason {
		return value
	}
	for n := maxMigrationIssueReason - 3; n > 0; n-- {
		if utf8.ValidString(value[:n]) {
			return value[:n] + "..."
		}
	}
	return "..."
}

// loadDescriptor bounds the scan pipeline: it computes one source tree at a
// time then drops file and changelog bodies before proceeding to the next row.
func (s *skillMigrationService) loadDescriptor(ctx context.Context, row sqlc.Skill) (skillMigrationRecord, error) {
	record, err := s.loadRecord(ctx, row)
	if err != nil {
		return skillMigrationRecord{}, err
	}
	record.files = nil
	record.changelog = nil
	return record, nil
}

func migrationUsageBySkill(rows []sqlc.Skill, usage []sqlc.SkillUsage) (map[string]sqlc.SkillUsage, []SkillMigrationIssue) {
	source := make(map[string]sqlc.Skill, len(rows))
	for _, row := range rows {
		source[row.ID] = row
	}
	bySkill := make(map[string]sqlc.SkillUsage, len(usage))
	issues := make([]SkillMigrationIssue, 0)
	for _, item := range usage {
		row, exists := source[item.SkillID]
		if !exists {
			issues = append(issues, SkillMigrationIssue{SkillID: item.SkillID, Kind: "unsupported", Reason: "usage has no source Skill"})
			continue
		}
		if _, duplicate := bySkill[item.SkillID]; duplicate {
			issues = append(issues, SkillMigrationIssue{SkillID: item.SkillID, Kind: "unsupported", Reason: "duplicate usage rows"})
			continue
		}
		if row.Scope != "user_agent" || item.UserID != row.UserID.String || item.AgentID != row.AgentID.String {
			issues = append(issues, SkillMigrationIssue{SkillID: item.SkillID, Kind: "unsupported", Reason: "usage owner cannot map to user_agent Skill"})
			continue
		}
		bySkill[item.SkillID] = item
	}
	return bySkill, issues
}

// canonicalMigrationUsage deliberately omits derived Home identity. The source
// aggregate protects runtime usage facts without treating this migration's own
// writes as source drift.
func canonicalMigrationUsage(item sqlc.SkillUsage) ([]byte, error) {
	return canonicalJSON(map[string]any{
		"skill_id": item.SkillID, "user_id": item.UserID, "agent_id": item.AgentID,
		"use_count": jsonNumber(item.UseCount), "last_used_at": formatUTCTimestamp(item.LastUsedAt.UTC()),
		"created_at": formatUTCTimestamp(item.CreatedAt.UTC()),
	})
}

func (s *skillMigrationService) loadRecord(ctx context.Context, row sqlc.Skill) (skillMigrationRecord, error) {
	root, err := migrationSkillRoot(row)
	if err != nil {
		return skillMigrationRecord{}, unsupportedSource("invalid source scope or owner", err)
	}
	if row.Name == "" {
		return skillMigrationRecord{}, unsupportedSource("source name is empty", nil)
	}
	if err := skillNameValidationError(row.Name, row.Name); err != nil {
		return skillMigrationRecord{}, unsupportedSource("source name is invalid", err)
	}
	if row.Status != SkillStatusActive && row.Status != SkillStatusDeprecated {
		return skillMigrationRecord{}, unsupportedSource("source status is invalid", nil)
	}
	if len(row.Description) > maxManagedFileBytes {
		return skillMigrationRecord{}, unsupportedSource("source description exceeds limit", nil)
	}
	if len(row.Metadata) > maxCatalogMetadataBytes {
		return skillMigrationRecord{}, unsupportedSource("source metadata exceeds limit", nil)
	}
	fileBounds, err := s.q.GetSkillMigrationFileBounds(ctx, row.ID)
	if err != nil {
		return skillMigrationRecord{}, fmt.Errorf("skills: bound migration files %q: %w", row.ID, err)
	}
	// Both publication trees add metadata; the archive also adds migration.json.
	if fileBounds.FileCount+2 > maxManagedTreeEntries || fileBounds.MaxContentBytes > maxManagedFileBytes || fileBounds.TotalContentBytes > maxManagedTreeBytes {
		return skillMigrationRecord{}, unsupportedSource("source files exceed publication limits", nil)
	}
	changeBounds, err := s.q.GetSkillMigrationChangelogBounds(ctx, row.ID)
	if err != nil {
		return skillMigrationRecord{}, fmt.Errorf("skills: bound migration changelog %q: %w", row.ID, err)
	}
	if changeBounds.ChangelogCount > maxMigrationChangelogRows || changeBounds.MaxContentBytes > maxCatalogMetadataBytes || changeBounds.TotalContentBytes > maxMigrationChangelogBytes {
		return skillMigrationRecord{}, unsupportedSource("source changelog exceeds archive limits", nil)
	}
	files, err := s.q.ListSkillFiles(ctx, row.ID)
	if err != nil {
		return skillMigrationRecord{}, err
	}
	treeFiles := make([]skillTreeEntry, 0, len(files))
	mainFound := false
	for _, f := range files {
		if !utf8.Valid(f.Content) || bytes.IndexByte(f.Content, 0) >= 0 {
			return skillMigrationRecord{}, unsupportedSource("source file content is not UTF-8 text", nil)
		}
		if err := validateSkillTreePath(f.Path); err != nil {
			return skillMigrationRecord{}, unsupportedSource("source file path is invalid", err)
		}
		treeFiles = append(treeFiles, skillTreeEntry{Path: f.Path, Content: append([]byte(nil), f.Content...), Mode: 0o644})
		if f.Path == MainFile && len(f.Content) > 0 {
			mainFound = true
		}
	}
	if !mainFound {
		return skillMigrationRecord{}, unsupportedSource("source has missing or empty SKILL.md", nil)
	}
	metadataValue, err := decodeStrictJSON(row.Metadata)
	if err != nil {
		return skillMigrationRecord{}, unsupportedSource("source metadata JSON is invalid", err)
	}
	metadata, ok := metadataValue.(map[string]any)
	if !ok {
		return skillMigrationRecord{}, unsupportedSource("source metadata must be an object", nil)
	}
	tree := skillTree{Metadata: skillMetadataEnvelope{Status: row.Status, DisableModelInvocation: row.DisableModelInvocation, Metadata: metadata, CreatedAt: row.CreatedAt.UTC(), UpdatedAt: row.UpdatedAt.UTC(), LegacyLifecycleVersion: row.Version}, Files: treeFiles}
	if err := validateMigrationTreeBounds(tree); err != nil {
		return skillMigrationRecord{}, unsupportedSource("source tree is invalid or exceeds limits", err)
	}
	activeDigest, err := digestSkillTree(tree)
	if err != nil {
		return skillMigrationRecord{}, unsupportedSource("source tree cannot be canonicalized", err)
	}
	changes, err := s.q.ListSkillMigrationChangelog(ctx, row.ID)
	if err != nil {
		return skillMigrationRecord{}, err
	}
	archiveName := migrationArchiveName(row.ID)
	archiveJSON, err := migrationArchiveJSON(row, changes)
	if err != nil {
		return skillMigrationRecord{}, unsupportedSource("source changelog JSON is invalid", err)
	}
	archiveFiles := append(append([]skillTreeEntry(nil), treeFiles...), skillTreeEntry{Path: "migration.json", Content: archiveJSON, Mode: 0o644})
	sort.Slice(archiveFiles, func(i, j int) bool { return archiveFiles[i].Path < archiveFiles[j].Path })
	archiveMetadata := tree.Metadata
	archiveMetadata.Metadata = map[string]any{"created_by": "stella_migration", "source_id": row.ID}
	archiveTree := skillTree{Metadata: archiveMetadata, Files: archiveFiles}
	if err := validateMigrationTreeBounds(archiveTree); err != nil {
		return skillMigrationRecord{}, unsupportedSource("source archive exceeds publication limits", err)
	}
	archiveDigest, err := digestSkillTree(archiveTree)
	if err != nil {
		return skillMigrationRecord{}, unsupportedSource("source archive cannot be canonicalized", err)
	}
	var fileBytes int64
	for _, file := range treeFiles {
		fileBytes += int64(len(file.Content))
	}
	return skillMigrationRecord{row: row, files: treeFiles, activeDigest: activeDigest, archiveDigest: archiveDigest, archiveName: archiveName, changelog: changes, root: root, fileCount: int64(len(treeFiles)), fileBytes: fileBytes}, nil
}

// validateMigrationTreeBounds applies the managed publication ceiling to the
// complete revision, including Stella's metadata control file and migration.json.
func validateMigrationTreeBounds(tree skillTree) error {
	metadata, err := encodeSkillMetadataEnvelope(tree.Metadata)
	if err != nil {
		return err
	}
	if len(tree.Files)+1 > maxManagedTreeEntries {
		return errors.New("skills: migration tree exceeds 512 source files including control files")
	}
	total := int64(len(metadata))
	if int64(len(metadata)) > maxManagedFileBytes {
		return errors.New("skills: migration metadata exceeds 8 MiB")
	}
	for _, file := range tree.Files {
		if int64(len(file.Content)) > maxManagedFileBytes {
			return fmt.Errorf("skills: migration source file %q exceeds 8 MiB", file.Path)
		}
		if strings.Count(file.Path, "/") > maxManagedTreeDepth {
			return fmt.Errorf("skills: migration source file %q exceeds directory depth 16", file.Path)
		}
		if total > maxManagedTreeBytes-int64(len(file.Content)) {
			return errors.New("skills: migration tree exceeds 32 MiB including control files")
		}
		total += int64(len(file.Content))
	}
	return nil
}

func migrationSkillRoot(row sqlc.Skill) (*home.SkillRoot, error) {
	user, agent := row.UserID.String, row.AgentID.String
	switch row.Scope {
	case "system":
		if user != "" || agent != "" {
			return nil, errors.New("skills: invalid system owner")
		}
		return home.SystemSkillCatalog(), nil
	case "system_agent":
		if user != "" || agent == "" {
			return nil, errors.New("skills: invalid system_agent owner")
		}
		return home.SystemAgentSkillCatalog(agent)
	case "user":
		if user == "" || agent != "" {
			return nil, errors.New("skills: invalid user owner")
		}
		return home.UserSkillCatalog(user)
	case "user_agent":
		if user == "" || agent == "" {
			return nil, errors.New("skills: invalid user_agent owner")
		}
		return home.UserAgentSkillCatalog(user, agent)
	default:
		return nil, fmt.Errorf("skills: unsupported migration scope %q", row.Scope)
	}
}

func migrationArchiveName(id string) string {
	h := sha256.Sum256([]byte(id))
	return "legacy-" + hex.EncodeToString(h[:16])
}

func migrationArchiveJSON(row sqlc.Skill, changes []sqlc.SkillChangelog) ([]byte, error) {
	changeValues := make([]any, 0, len(changes))
	for _, c := range changes {
		meta, err := decodeStrictJSON(c.Metadata)
		if err != nil {
			return nil, err
		}
		changeValues = append(changeValues, map[string]any{"id": c.ID, "skill_id": c.SkillID, "user_id": nullableJSON(c.UserID), "agent_id": nullableJSON(c.AgentID), "scope": c.Scope, "action": c.Action, "version_before": nullableJSON(c.VersionBefore), "version_after": jsonNumber(c.VersionAfter), "metadata": meta, "created_at": formatUTCTimestamp(c.CreatedAt.UTC())})
	}
	meta, err := decodeStrictJSON(row.Metadata)
	if err != nil {
		return nil, err
	}
	return canonicalJSON(map[string]any{"layout": skillHomeAuthorityLayout, "identity": map[string]any{"id": row.ID, "scope": row.Scope, "user_id": nullableJSON(row.UserID), "agent_id": nullableJSON(row.AgentID), "name": row.Name}, "description": row.Description, "status": row.Status, "disable_model_invocation": row.DisableModelInvocation, "metadata": meta, "created_at": formatUTCTimestamp(row.CreatedAt.UTC()), "updated_at": formatUTCTimestamp(row.UpdatedAt.UTC()), "legacy_lifecycle_version": jsonNumber(row.Version), "changelog": changeValues})
}

func nullableJSON(v any) any {
	switch x := v.(type) {
	case pgtype.Text:
		if x.Valid {
			return x.String
		}
	case pgtype.Int8:
		if x.Valid {
			return jsonNumber(x.Int64)
		}
	}
	return nil
}

func (s *skillMigrationService) publishRecord(ctx context.Context, record skillMigrationRecord) (bool, error) {
	row, err := s.q.GetSkillByID(ctx, record.row.ID)
	if err != nil {
		return false, fmt.Errorf("skills: reload migration source %q: %w", record.row.ID, err)
	}
	fresh, err := s.loadRecord(ctx, row)
	if err != nil {
		return false, err
	}
	if !sameSkillMigrationRecord(record, fresh) {
		return false, fmt.Errorf("skills: source %q changed before publication", record.row.ID)
	}
	didPublish := false
	if fresh.row.Status == SkillStatusActive {
		existing, err := s.exactMigrationTarget(ctx, fresh.root, sandbox.PathWorkspace, fresh.row.Name, fresh.activeDigest)
		if err != nil {
			return false, err
		}
		if err := s.publish(ctx, fresh.root, sandbox.PathWorkspace, fresh.row.Name, fresh.activeDigest, fresh.row, fresh.files, nil); err != nil {
			return false, err
		}
		didPublish = !existing
	}
	archiveCatalog := path.Join(sandbox.PathWorkspace, skillMigrationArchiveRoot)
	existingArchive, err := s.exactMigrationTarget(ctx, fresh.root, archiveCatalog, fresh.archiveName, fresh.archiveDigest)
	if err != nil {
		return didPublish, err
	}
	if err := s.publish(ctx, fresh.root, archiveCatalog, fresh.archiveName, fresh.archiveDigest, fresh.row, fresh.files, fresh.changelog); err != nil {
		return didPublish, err
	}
	return didPublish || !existingArchive, nil
}

// preflightTargets completes every read-only target check before publication.
// A missing Home or entry is clean: publication owns creating both later.
func (s *skillMigrationService) preflightTargets(ctx context.Context, records []skillMigrationRecord, summary *SkillMigrationSummary) error {
	for _, record := range records {
		if record.row.Status == SkillStatusActive {
			if reason, err := s.migrationTargetConflict(ctx, record.root, sandbox.PathWorkspace, record.row.Name, record.activeDigest); err != nil {
				return err
			} else if reason != "" {
				summary.addIssue(SkillMigrationIssue{SkillID: record.row.ID, Kind: "conflict", Reason: reason})
			}
		}
		if reason, err := s.migrationTargetConflict(ctx, record.root, path.Join(sandbox.PathWorkspace, skillMigrationArchiveRoot), record.archiveName, record.archiveDigest); err != nil {
			return err
		} else if reason != "" {
			summary.addIssue(SkillMigrationIssue{SkillID: record.row.ID, Kind: "conflict", Reason: reason})
		}
	}
	return nil
}

func (s *skillMigrationService) migrationTargetConflict(ctx context.Context, root *home.SkillRoot, catalog, name, digest string) (reason string, err error) {
	exists, err := s.homes.UseExistingSkillFilesystem(ctx, root, func(fsys sandbox.Filesystem) error {
		inspector, ok := fsys.(sandbox.ManagedSkillTargetInspector)
		if !ok {
			return errors.New("home filesystem cannot inspect managed Skills")
		}
		entry := path.Join(catalog, name)
		target, inspectErr := inspector.InspectManagedSkillTarget(ctx, entry)
		if inspectErr != nil {
			if info, statErr := fsys.Stat(ctx, entry); statErr == nil && !info.IsDir {
				reason = "target is occupied by an unmanaged entry"
				return nil
			}
			return fmt.Errorf("inspect target: %w", inspectErr)
		}
		if target.Managed {
			if target.Digest != digest {
				reason = "target selects a different managed revision"
				return nil
			}
			if verifyErr := verifyManagedRevision(ctx, fsys, path.Join(catalog, ".stella-revisions", name, digest), digest); verifyErr != nil {
				var invalid *managedRevisionInvalidError
				if !errors.As(verifyErr, &invalid) {
					return verifyErr
				}
				reason = "target's selected managed revision is invalid"
			}
			return nil
		}
		if _, statErr := fsys.Stat(ctx, entry); errors.Is(statErr, fs.ErrNotExist) {
			return nil
		} else if statErr != nil {
			return fmt.Errorf("stat target: %w", statErr)
		}
		reason = "target is occupied by an unmanaged entry"
		return nil
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !exists {
		return "", nil
	}
	return reason, err
}

// exactMigrationTarget distinguishes a verified idempotent target from a
// publication attempt without treating ordinary pre-publication conflicts as
// externally visible outcomes; publish performs the authoritative conflict check.
func (s *skillMigrationService) exactMigrationTarget(ctx context.Context, root *home.SkillRoot, catalog, name, digest string) (bool, error) {
	exact := false
	exists, err := s.homes.UseExistingSkillFilesystem(ctx, root, func(fsys sandbox.Filesystem) error {
		inspector, ok := fsys.(sandbox.ManagedSkillTargetInspector)
		if !ok {
			return errors.New("skills: Home filesystem cannot inspect managed Skills")
		}
		target, err := inspector.InspectManagedSkillTarget(ctx, path.Join(catalog, name))
		if err != nil {
			return err
		}
		if !target.Managed || target.Digest != digest {
			return nil
		}
		if err := verifyManagedRevision(ctx, fsys, path.Join(catalog, ".stella-revisions", name, digest), digest); err != nil {
			return err
		}
		exact = true
		return nil
	})
	if !exists && err == nil {
		return false, nil
	}
	return exact, err
}

func sameSkillMigrationRecord(want, got skillMigrationRecord) bool {
	return want.row.ID == got.row.ID && want.row.Scope == got.row.Scope && want.row.UserID == got.row.UserID && want.row.AgentID == got.row.AgentID && want.row.Name == got.row.Name && want.activeDigest == got.activeDigest && want.archiveDigest == got.archiveDigest && want.archiveName == got.archiveName && want.fileCount == got.fileCount && want.fileBytes == got.fileBytes
}

func (s *skillMigrationService) publish(ctx context.Context, root *home.SkillRoot, catalog, name, digest string, row sqlc.Skill, files []skillTreeEntry, changes []sqlc.SkillChangelog) error {
	return s.homes.UseSkillFilesystem(ctx, root, func(fsys sandbox.Filesystem) error {
		inspector, ok := fsys.(sandbox.ManagedSkillTargetInspector)
		if !ok {
			return errors.New("skills: Home filesystem cannot inspect managed Skills")
		}
		entry := path.Join(catalog, name)
		target, err := inspector.InspectManagedSkillTarget(ctx, entry)
		if err != nil {
			return fmt.Errorf("skills: inspect migration target: %w", err)
		}
		if target.Managed {
			if target.Digest == digest {
				if err := verifyManagedRevision(ctx, fsys, path.Join(catalog, ".stella-revisions", name, digest), digest); err != nil {
					return fmt.Errorf("skills: verify existing migration target %q: %w", entry, err)
				}
				return nil
			}
			return fmt.Errorf("skills: migration target conflict for %q", entry)
		}
		if _, err := fsys.Stat(ctx, entry); !errors.Is(err, fs.ErrNotExist) {
			if err != nil {
				return err
			}
			return fmt.Errorf("skills: migration target conflict for %q", entry)
		}
		metadata, err := decodeStrictJSON(row.Metadata)
		if err != nil {
			return err
		}
		m, ok := metadata.(map[string]any)
		if !ok {
			return errors.New("skills: migration metadata must be an object")
		}
		if catalog != sandbox.PathWorkspace {
			m = map[string]any{"created_by": "stella_migration", "source_id": row.ID}
			archiveJSON, err := migrationArchiveJSON(row, changes)
			if err != nil {
				return err
			}
			files = append(append([]skillTreeEntry(nil), files...), skillTreeEntry{Path: "migration.json", Content: archiveJSON, Mode: 0o644})
			sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
		}
		env := skillMetadataEnvelope{Status: row.Status, DisableModelInvocation: row.DisableModelInvocation, Metadata: m, CreatedAt: row.CreatedAt.UTC(), UpdatedAt: row.UpdatedAt.UTC(), LegacyLifecycleVersion: row.Version}
		if _, err := encodeSkillMetadataEnvelope(env); err != nil {
			return err
		}
		publication := skillTreePublication(skillTree{Metadata: env, Files: files})
		publisher, ok := fsys.(sandbox.ManagedSkillPublisher)
		if !ok {
			return errors.New("skills: Home filesystem cannot publish managed Skills")
		}
		if err := publisher.PublishManagedSkill(ctx, catalog, name, digest, publication); err != nil {
			return fmt.Errorf("%w: publish migration target %q: %w", sandbox.ErrOutcomeUnknown, entry, err)
		}
		selected, err := inspector.InspectManagedSkillTarget(ctx, entry)
		if err != nil || !selected.Managed || selected.Digest != digest {
			return fmt.Errorf("%w: verify migration target %q", sandbox.ErrOutcomeUnknown, entry)
		}
		if err := verifyManagedRevision(ctx, fsys, path.Join(catalog, ".stella-revisions", name, selected.Digest), digest); err != nil {
			return fmt.Errorf("%w: verify published migration target %q: %w", sandbox.ErrOutcomeUnknown, entry, err)
		}
		return nil
	})
}

func (s *skillMigrationService) updateUsage(ctx context.Context, r skillMigrationRecord) error {
	if !r.hasUsage {
		return nil
	}
	if r.row.Scope != "user_agent" {
		return errors.New("skills: non-user_agent Skill has usage")
	}
	n, err := s.q.UpdateSkillUsageHomeIdentity(ctx, sqlc.UpdateSkillUsageHomeIdentityParams{SkillID: r.row.ID, UserID: r.row.UserID.String, AgentID: r.row.AgentID.String, Scope: pgtype.Text{String: r.row.Scope, Valid: true}, Name: pgtype.Text{String: r.row.Name, Valid: true}, LastContentDigest: pgtype.Text{String: r.activeDigest, Valid: true}})
	if err != nil {
		return fmt.Errorf("%w: update usage identity %q: %w", sandbox.ErrOutcomeUnknown, r.row.ID, err)
	}
	if n != 1 {
		if n == 0 {
			return fmt.Errorf("skills: usage %q disappeared before update", r.row.ID)
		}
		return fmt.Errorf("%w: expected one usage row for %q, updated %d", sandbox.ErrOutcomeUnknown, r.row.ID, n)
	}
	got, err := s.q.GetSkillUsageForUpdate(ctx, sqlc.GetSkillUsageForUpdateParams{SkillID: r.row.ID, UserID: r.row.UserID.String, AgentID: r.row.AgentID.String})
	if err != nil {
		return fmt.Errorf("%w: read migrated usage %q: %w", sandbox.ErrOutcomeUnknown, r.row.ID, err)
	}
	if got.SkillID != r.usage.SkillID || got.UserID != r.usage.UserID || got.AgentID != r.usage.AgentID || got.UseCount != r.usage.UseCount || !got.LastUsedAt.UTC().Equal(r.usage.LastUsedAt.UTC()) || !got.CreatedAt.UTC().Equal(r.usage.CreatedAt.UTC()) || !got.Scope.Valid || got.Scope.String != r.row.Scope || !got.Name.Valid || got.Name.String != r.row.Name || !got.LastContentDigest.Valid || got.LastContentDigest.String != r.activeDigest {
		return fmt.Errorf("%w: migrated usage %q differs on readback", sandbox.ErrOutcomeUnknown, r.row.ID)
	}
	return nil
}

func (s *skillMigrationService) verify(ctx context.Context, records []skillMigrationRecord) error {
	for _, r := range records {
		if r.row.Status == SkillStatusActive {
			if err := s.verifyTarget(ctx, r.root, sandbox.PathWorkspace, r.row.Name, r.activeDigest); err != nil {
				return err
			}
		}
		if err := s.verifyTarget(ctx, r.root, path.Join(sandbox.PathWorkspace, skillMigrationArchiveRoot), r.archiveName, r.archiveDigest); err != nil {
			return err
		}
	}
	return nil
}

// verifyUsage is read-only and is used on completed reruns as well as the
// final pass, so a completed migration never repairs derived usage identity.
func (s *skillMigrationService) verifyUsage(ctx context.Context, records []skillMigrationRecord) error {
	for _, r := range records {
		if !r.hasUsage {
			continue
		}
		got, err := s.q.GetSkillUsageForUpdate(ctx, sqlc.GetSkillUsageForUpdateParams{SkillID: r.row.ID, UserID: r.row.UserID.String, AgentID: r.row.AgentID.String})
		if err != nil {
			return fmt.Errorf("skills: read verified usage %q: %w", r.row.ID, err)
		}
		if got.SkillID != r.usage.SkillID || got.UserID != r.usage.UserID || got.AgentID != r.usage.AgentID || got.UseCount != r.usage.UseCount || !got.LastUsedAt.UTC().Equal(r.usage.LastUsedAt.UTC()) || !got.CreatedAt.UTC().Equal(r.usage.CreatedAt.UTC()) || !got.Scope.Valid || got.Scope.String != r.row.Scope || !got.Name.Valid || got.Name.String != r.row.Name || !got.LastContentDigest.Valid || got.LastContentDigest.String != r.activeDigest {
			return fmt.Errorf("skills: verified usage %q differs", r.row.ID)
		}
	}
	return nil
}

// verifyTarget is read-only: completed migrations must never repair a Home.
func (s *skillMigrationService) verifyTarget(ctx context.Context, root *home.SkillRoot, catalog, name, want string) error {
	exists, err := s.homes.UseExistingSkillFilesystem(ctx, root, func(fsys sandbox.Filesystem) error {
		inspector, ok := fsys.(sandbox.ManagedSkillTargetInspector)
		if !ok {
			return errors.New("skills: Home filesystem cannot inspect managed Skills")
		}
		entry := path.Join(catalog, name)
		target, err := inspector.InspectManagedSkillTarget(ctx, entry)
		if err != nil {
			return fmt.Errorf("skills: inspect verified target: %w", err)
		}
		if !target.Managed || target.Digest != want {
			return fmt.Errorf("skills: verified target %q differs", entry)
		}
		// The direct entry is mutable. Verify exactly the revision selected by
		// inspection, never a second traversal through that link.
		revision := path.Join(catalog, ".stella-revisions", name, target.Digest)
		if err := verifyManagedRevision(ctx, fsys, revision, want); err != nil {
			return fmt.Errorf("skills: verify managed revision %q: %w", entry, err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if !exists {
		return errors.New("skills: verified Skill Home is absent")
	}
	return nil
}

func (s *skillMigrationService) marker(ctx context.Context, dry bool) (sqlc.StorageMigration, error) {
	m, err := s.q.GetStorageMigration(ctx, SkillHomeAuthorityMigration)
	if err == nil {
		if m.Name != SkillHomeAuthorityMigration || m.ObjectAuthorityConfigured || (m.State != "pending" && m.State != "completed") {
			return m, errors.New("skills: malformed Skill migration marker")
		}
		if m.State == "pending" {
			if err := validateSkillMigrationMarker(m, []byte(`{}`)); err != nil {
				return m, err
			}
		}
		return m, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return m, err
	}
	if dry {
		return sqlc.StorageMigration{Name: SkillHomeAuthorityMigration, State: "pending"}, nil
	}
	m, err = s.q.CreateSkillAuthorityStorageMigration(ctx, sqlc.CreateSkillAuthorityStorageMigrationParams{Name: SkillHomeAuthorityMigration, Metadata: []byte(`{}`)})
	if err == nil {
		return m, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return m, err
	}
	m, err = s.q.GetStorageMigration(ctx, SkillHomeAuthorityMigration)
	if err != nil {
		return m, err
	}
	if err := validateSkillMigrationMarker(m, []byte(`{}`)); err != nil {
		return m, err
	}
	return m, nil
}

func validateSkillMigrationMarker(marker sqlc.StorageMigration, expected []byte) error {
	if marker.Name != SkillHomeAuthorityMigration || marker.ObjectAuthorityConfigured || (marker.State != "pending" && marker.State != "completed") {
		return errors.New("skills: malformed Skill migration marker")
	}
	value, err := decodeStrictJSON(marker.Metadata)
	if err != nil {
		return fmt.Errorf("skills: malformed Skill migration marker metadata: %w", err)
	}
	canonical, err := canonicalJSON(value)
	// jsonb has already normalized PostgreSQL's stored representation; compare
	// the exact canonical bytes rather than its presentation whitespace.
	if err != nil || !bytes.Equal(canonical, expected) {
		return errors.New("skills: Skill migration marker metadata differs from canonical expected bytes")
	}
	return nil
}

func sameSkillMigrationAggregate(a, b SkillMigrationSummary) bool {
	return a.SourceCount == b.SourceCount && a.Files == b.Files && a.Bytes == b.Bytes && a.SHA256 == b.SHA256 &&
		a.ActiveCount == b.ActiveCount && a.ArchiveCount == b.ArchiveCount && a.UsageCount == b.UsageCount &&
		a.UnsupportedCount == b.UnsupportedCount && a.ConflictCount == b.ConflictCount
}

func skillMigrationMetadata(summary SkillMigrationSummary) []byte {
	encoded, err := canonicalJSON(map[string]any{"layout": skillHomeAuthorityLayout, "source_count": jsonNumber(summary.SourceCount), "files": jsonNumber(summary.Files), "bytes": jsonNumber(summary.Bytes), "sha256": summary.SHA256, "active_count": jsonNumber(summary.ActiveCount), "archive_count": jsonNumber(summary.ArchiveCount), "usage_count": jsonNumber(summary.UsageCount)})
	if err != nil {
		panic(err)
	}
	return encoded
}
