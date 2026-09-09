// Package resourceupgrade owns the one-way migration from legacy database
// plugin and Skill rows to scoped filesystem resources. The composition root
// only supplies domain services and a Home opener; durable manifest state,
// advisory locking, retry, and commit fencing stay inside this package.
package resourceupgrade

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/mcp"
	"github.com/CherryHQ/stella/internal/platform/home"
	"github.com/CherryHQ/stella/internal/plugin"
	"github.com/CherryHQ/stella/internal/skill"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

const filesystemMigrationKey = "filesystem_resources_v1"

const (
	statePrepared  = "prepared"
	stateCompleted = "completed"
)

// State is the narrow startup projection of the durable migration marker.
// Manifest identities and fingerprints remain private to this package.
type State string

// Completed reports whether filesystem resources are the durable authority.
func (s State) Completed() bool { return string(s) == stateCompleted }

// Dependencies are the legacy readers and file-backed services required by
// the cutover. They are deliberately typed at the domain boundary so callers
// cannot reach the coordinator's manifest or transaction internals.
type Dependencies struct {
	DB            *pgxpool.Pool
	Roots         home.RootOpener
	LegacyPlugins *plugin.LegacyService
	LegacySkills  *skill.LegacySkillStore
	MCPService    *mcp.Service
	Checkpoint    func(string) error
}

// The durable manifest contains identities and fingerprints, never resource
// bytes or credentials. Legacy rows remain the recovery source until complete;
// after completion no startup may reconstruct files from those rows.
type filesystemMigrationManifest struct {
	Version      int                           `json:"version"`
	State        string                        `json:"state"`
	PluginDigest string                        `json:"plugin_digest"`
	Plugins      []filesystemMigrationFile     `json:"plugins"`
	Skills       []filesystemMigrationFile     `json:"skills"`
	MCP          []plugin.MCPMigrationEvidence `json:"mcp"`
	Settings     []plugin.LegacySettingsExport `json:"settings"`
}

type filesystemMigrationFile struct {
	ID           string `json:"id"`
	LegacyID     string `json:"legacy_id,omitempty"`
	SourceDigest string `json:"source_digest"`
	TargetDigest string `json:"target_digest"`
}

func readFilesystemMigration(ctx context.Context, q *sqlc.Queries) (filesystemMigrationManifest, string, error) {
	row, err := q.GetSetting(ctx, filesystemMigrationKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return filesystemMigrationManifest{}, "", nil
	}
	if err != nil {
		return filesystemMigrationManifest{}, "", err
	}
	var manifest filesystemMigrationManifest
	if len(row.Value) > 16<<20 || json.Unmarshal([]byte(row.Value), &manifest) != nil || manifest.Version != 1 ||
		(manifest.State != statePrepared && manifest.State != stateCompleted) || !strings.HasPrefix(manifest.PluginDigest, "sha256:") {
		return filesystemMigrationManifest{}, "", errors.New("invalid filesystem resource migration manifest")
	}
	return manifest, row.Value, nil
}

// ReadState reads only the durable migration state needed by startup wiring.
// An absent marker is the pending state and is safe to retry.
func ReadState(ctx context.Context, db *pgxpool.Pool) (State, error) {
	if db == nil {
		return "", errors.New("resource upgrade: database is required")
	}
	manifest, _, err := readFilesystemMigration(ctx, sqlc.New(db))
	return State(manifest.State), err
}

func makeFilesystemManifest(plugins plugin.LegacyFileExport, skills skill.PreparedLegacyFileExport) filesystemMigrationManifest {
	manifest := filesystemMigrationManifest{Version: 1, State: statePrepared, PluginDigest: plugins.Digest(), MCP: plugins.MCPMappings, Settings: plugins.Settings}
	for _, entry := range plugins.Entries {
		manifest.Plugins = append(manifest.Plugins, filesystemMigrationFile{ID: entry.Key.ID(), SourceDigest: entry.SourceDigest, TargetDigest: plugin.PreparedFileDigest(entry.Files)})
	}
	for _, entry := range skills.Skills {
		files := make(map[string]plugin.ResourceFile, len(entry.Files))
		for name, file := range entry.Files {
			files[name] = plugin.ResourceFile{Data: file.Content, Mode: file.Mode}
		}
		manifest.Skills = append(manifest.Skills, filesystemMigrationFile{ID: entry.FileSkillID, LegacyID: entry.SourceIdentityID, SourceDigest: entry.SourceDigest, TargetDigest: plugin.PreparedFileDigest(files)})
	}
	for _, entries := range [][]filesystemMigrationFile{manifest.Plugins, manifest.Skills} {
		slices.SortFunc(entries, func(a, b filesystemMigrationFile) int { return strings.Compare(a.ID, b.ID) })
	}
	return manifest
}

// Both domains write the same settings.json. Merge their negative declarations
// before taking the manifest fingerprint so publication never signs one policy
// and then silently broadens or replaces it during a retry.
func mergeSkillMigrationSettings(ctx context.Context, store *plugin.ResourceStore, plugins *plugin.LegacyFileExport, skills skill.PreparedLegacyFileExport) error {
	for _, setting := range skills.Settings {
		index := slices.IndexFunc(plugins.Settings, func(current plugin.LegacySettingsExport) bool {
			return string(current.Scope) == setting.Scope && current.UserID == setting.UserID && current.AgentID == setting.AgentID
		})
		if index < 0 {
			current, digest, err := store.ReadSettings(ctx, plugin.Scope(setting.Scope), setting.UserID, setting.AgentID)
			if err != nil {
				return err
			}
			plugins.Settings = append(plugins.Settings, plugin.LegacySettingsExport{Scope: plugin.Scope(setting.Scope), UserID: setting.UserID, AgentID: setting.AgentID, Settings: current, SourceDigest: digest})
			index = len(plugins.Settings) - 1
		}
		current := &plugins.Settings[index].Settings
		current.Disabled = append(current.Disabled, setting.Disabled...)
		current.Forbidden = append(current.Forbidden, setting.Forbidden...)
		slices.Sort(current.Disabled)
		current.Disabled = slices.Compact(current.Disabled)
		slices.Sort(current.Forbidden)
		current.Forbidden = slices.Compact(current.Forbidden)
		for key, tools := range current.DisabledTools {
			slices.Sort(tools)
			current.DisabledTools[key] = slices.Compact(tools)
		}
	}
	return nil
}

// A retry may observe either the original settings or the published target.
// Keep the original CAS digest in the manifest, and reject edits to either
// policy instead of absorbing them into a newly prepared migration.
func retainMigrationSettings(ctx context.Context, store *plugin.ResourceStore, prepared *plugin.LegacyFileExport, retained []plugin.LegacySettingsExport) error {
	if len(prepared.Settings) != len(retained) {
		return errors.New("filesystem migration settings owners changed")
	}
	for i := range prepared.Settings {
		setting := &prepared.Settings[i]
		index := slices.IndexFunc(retained, func(prior plugin.LegacySettingsExport) bool {
			return prior.Scope == setting.Scope && prior.UserID == setting.UserID && prior.AgentID == setting.AgentID
		})
		if index < 0 {
			return errors.New("filesystem migration settings owner changed")
		}
		prior := retained[index]
		current, digest, err := store.ReadSettings(ctx, setting.Scope, setting.UserID, setting.AgentID)
		if err != nil {
			return err
		}
		target, _ := json.Marshal(prior.Settings)
		observed, _ := json.Marshal(current)
		desired, _ := json.Marshal(setting.Settings)
		if !bytes.Equal(target, desired) || (digest != prior.SourceDigest && !bytes.Equal(observed, target)) {
			return errors.New("filesystem migration settings changed; retained manifest requires reconciliation")
		}
		setting.SourceDigest = prior.SourceDigest
	}
	return nil
}

// Run performs the complete retryable legacy-to-filesystem cutover. It owns
// the advisory lock, durable prepared/completed manifest, publication fences,
// evidence transaction, and uncertain-commit recovery.
func Run(ctx context.Context, deps Dependencies) (resultErr error) {
	db := deps.DB
	roots := deps.Roots
	legacyPlugins := deps.LegacyPlugins
	legacySkills := deps.LegacySkills
	mcpService := deps.MCPService
	checkpoint := deps.Checkpoint
	if db == nil {
		return errors.New("resource upgrade: database is required")
	}
	conn, err := db.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended($1, 0))`, filesystemMigrationKey); err != nil {
		return err
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := conn.Exec(unlockCtx, `SELECT pg_advisory_unlock(hashtextextended($1, 0))`, filesystemMigrationKey)
		if err != nil {
			// Never return a session-level lock to the connection pool.
			_ = conn.Conn().Close(unlockCtx)
			resultErr = errors.Join(resultErr, err)
		}
	}()
	q := sqlc.New(conn)
	existing, existingRaw, err := readFilesystemMigration(ctx, q)
	if err != nil || existing.State == stateCompleted {
		return err
	}
	if roots == nil || legacyPlugins == nil || legacySkills == nil || mcpService == nil {
		return errors.New("resource upgrade: incomplete dependencies")
	}
	resources := plugin.NewResourceStore(roots)
	retained := existing
	prepare := func() (plugin.LegacyFileExport, skill.PreparedLegacyFileExport, filesystemMigrationManifest, error) {
		plugins, err := legacyPlugins.PrepareLegacyFileExport(ctx, resources)
		if err != nil {
			return plugins, skill.PreparedLegacyFileExport{}, filesystemMigrationManifest{}, err
		}
		skills, err := legacySkills.PrepareLegacyFileExport(ctx)
		if err != nil {
			return plugins, skills, filesystemMigrationManifest{}, err
		}
		if err := mergeSkillMigrationSettings(ctx, resources, &plugins, skills); err != nil {
			return plugins, skills, filesystemMigrationManifest{}, err
		}
		if retained.State != "" {
			if err := retainMigrationSettings(ctx, resources, &plugins, retained.Settings); err != nil {
				return plugins, skills, filesystemMigrationManifest{}, err
			}
		}
		return plugins, skills, makeFilesystemManifest(plugins, skills), nil
	}
	plugins, skills, manifest, err := prepare()
	if err != nil {
		return fmt.Errorf("prepare filesystem resources: %w", err)
	}
	raw, err := json.Marshal(manifest)
	if err != nil || len(raw) > 16<<20 {
		return errors.New("filesystem resource migration manifest exceeds its limit")
	}
	if existingRaw != "" {
		previous, _ := json.Marshal(existing)
		if !bytes.Equal(previous, raw) {
			return errors.New("filesystem resource migration source or target changed; retained manifest requires reconciliation")
		}
	} else {
		changed, err := q.UpsertSettingIfValue(ctx, sqlc.UpsertSettingIfValueParams{Key: filesystemMigrationKey, Value: string(raw)})
		if err != nil || changed != 1 {
			return errors.Join(errors.New("persist filesystem resource migration manifest"), err)
		}
	}
	check := func(stage string) error {
		if checkpoint != nil {
			return checkpoint(stage)
		}
		return nil
	}
	if err := check("prepared"); err != nil {
		return err
	}
	retained = manifest
	if err := legacyPlugins.PublishLegacyFileExport(ctx, resources, plugins); err != nil {
		return fmt.Errorf("publish plugin resources: %w", err)
	}
	if err := skill.PublishLegacyFileExport(ctx, roots, skills); err != nil {
		return fmt.Errorf("publish Skill resources: %w", err)
	}
	if err := check("published"); err != nil {
		return err
	}
	if err := legacyPlugins.VerifyLegacyFileExport(ctx, resources, plugins); err != nil {
		return fmt.Errorf("verify plugin resources: %w", err)
	}
	_, _, rechecked, err := prepare()
	if err != nil {
		return fmt.Errorf("recheck filesystem migration source: %w", err)
	}
	recheckedRaw, _ := json.Marshal(rechecked)
	if !bytes.Equal(raw, recheckedRaw) {
		return errors.New("filesystem migration source changed during publication")
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err := mcpService.MigrateLegacyFileCredentials(ctx, tx, resources, plugins.MCPMappings); err != nil {
		return fmt.Errorf("migrate MCP authorization: %w", err)
	}
	if err := skill.FinalizeLegacyFileEvidence(ctx, tx, roots, skills); err != nil {
		return fmt.Errorf("migrate Skill evidence: %w", err)
	}
	if err := check("evidence"); err != nil {
		return err
	}
	manifest.State = stateCompleted
	completed, _ := json.Marshal(manifest)
	changed, err := sqlc.New(tx).UpsertSettingIfValue(ctx, sqlc.UpsertSettingIfValueParams{Key: filesystemMigrationKey, Value: string(completed), ExpectedValue: string(raw)})
	if err != nil || changed != 1 {
		return errors.Join(errors.New("complete filesystem resource migration manifest"), err)
	}
	if err := tx.Commit(ctx); err != nil {
		// An uncertain acknowledgement never starts a mixed runtime. A fresh
		// startup reads the marker and either skips or retries the whole cutover.
		return fmt.Errorf("filesystem migration commit outcome unknown; restart to reconcile: %w", err)
	}
	return nil
}
