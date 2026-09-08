package plugin

// This file contains the one-way bridge from the legacy database plugin rows
// to the scoped resource filesystem.  It intentionally has no database write
// path: preparing an export is a read-only operation and publication is an
// idempotent, no-replace filesystem operation.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/internal/platform/home"
	"github.com/CherryHQ/stella/internal/plugin/agentpackage"
)

// LegacyFileExport is the complete, immutable write set produced by the
// legacy migration preflight.  File bytes are deliberately kept out of SQL.
type LegacyFileExport struct {
	Entries     []LegacyFileExportEntry
	Settings    []LegacySettingsExport
	MCPMappings []MCPMigrationEvidence
}

type LegacyFileExportEntry struct {
	Key          ResourceKey
	Files        map[string]ResourceFile
	SourceDigest string
}

// LegacySettingsExport contains one complete settings.json projection per
// owner. SourceDigest is the digest observed while preparing the export.
type LegacySettingsExport struct {
	Scope        Scope
	UserID       string
	AgentID      string
	Settings     ResourceSettings
	SourceDigest string
}

type legacyOwnerState struct {
	settings ResourceSettings
	digest   string
}

var ErrLegacyFileExportConflict = fmt.Errorf("%w: legacy filesystem export conflict", ErrLegacyMigrationConflict)

// PreparedFileDigest is the stable digest used for an export. It hashes the
// relative path, bytes, and executable bit of every file. It must never use
// CaptureResource's zip digest, whose archive metadata is an implementation
// detail.
func PreparedFileDigest(files map[string]ResourceFile) string {
	h := sha256.New()
	for _, name := range slices.Sorted(maps.Keys(files)) {
		file := files[name]
		mode := file.Mode.Perm() & 0o111
		content := sha256.Sum256(file.Data)
		_, _ = fmt.Fprintf(h, "%s\x00%d\x00%d\x00%s\n", name, mode, len(file.Data), hex.EncodeToString(content[:]))
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

func legacyExportDigest(export LegacyFileExport) string {
	h := sha256.New()
	entries := slices.Clone(export.Entries)
	slices.SortFunc(entries, func(a, b LegacyFileExportEntry) int {
		return strings.Compare(a.Key.ID(), b.Key.ID())
	})
	for _, entry := range entries {
		_, _ = fmt.Fprintf(h, "entry\x00%s\x00%s\x00%s\n", entry.Key.ID(), entry.SourceDigest, PreparedFileDigest(entry.Files))
	}
	settings := slices.Clone(export.Settings)
	slices.SortFunc(settings, func(a, b LegacySettingsExport) int {
		return strings.Compare(legacyOwnerID(a.Scope, a.UserID, a.AgentID), legacyOwnerID(b.Scope, b.UserID, b.AgentID))
	})
	for _, setting := range settings {
		encoded, _ := json.Marshal(canonicalSettings(setting.Settings))
		_, _ = fmt.Fprintf(h, "settings\x00%s\x00%s\x00%s\n", legacyOwnerID(setting.Scope, setting.UserID, setting.AgentID), setting.SourceDigest, encoded)
	}
	mappings := slices.Clone(export.MCPMappings)
	slices.SortFunc(mappings, func(a, b MCPMigrationEvidence) int {
		return strings.Compare(a.OldConfigID+"\x00"+a.OldServerKey, b.OldConfigID+"\x00"+b.OldServerKey)
	})
	for _, mapping := range mappings {
		_, _ = fmt.Fprintf(h, "mcp\x00%s\x00%s\x00%s\x00%s\n", mapping.OldConfigID, mapping.OldServerKey, mapping.NewResource.ID(), mapping.OldCredential)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// Digest is the compatibility spelling used by migration coordinators.
func (e LegacyFileExport) Digest() string { return legacyExportDigest(e) }

// PrepareLegacyFileExport reads unified legacy rows and produces a complete
// filesystem write set. It never writes a file or a database row.
func (s *LegacyService) PrepareLegacyFileExport(ctx context.Context, store *ResourceStore) (LegacyFileExport, error) {
	if s == nil || s.q == nil || store == nil {
		return LegacyFileExport{}, ErrLegacyMigrationConflict
	}
	if err := contextError(ctx); err != nil {
		return LegacyFileExport{}, err
	}
	rows, err := s.q.ListPluginDefinitions(ctx)
	if err != nil {
		return LegacyFileExport{}, fmt.Errorf("list plugin definitions: %w", err)
	}
	owners := map[string]*legacyOwnerState{}
	result := LegacyFileExport{}
	toolPolicies, err := s.legacyToolPolicies(ctx)
	if err != nil {
		return LegacyFileExport{}, err
	}
	ensureOwner := func(scope Scope, userID, agentID string) (*legacyOwnerState, error) {
		owner := legacyOwnerID(scope, userID, agentID)
		if state := owners[owner]; state != nil {
			return state, nil
		}
		settings, digest, readErr := store.ReadSettings(ctx, scope, userID, agentID)
		if readErr != nil {
			return nil, fmt.Errorf("read settings for %s: %w", owner, readErr)
		}
		state := &legacyOwnerState{settings: settings, digest: digest}
		owners[owner] = state
		return state, nil
	}
	for _, row := range rows {
		if row.RetiredAt.Valid {
			continue
		}
		definition := fromSQLDefinition(row)
		if definition.Source != SourceBuiltin && definition.Source != SourceCustom {
			return LegacyFileExport{}, fmt.Errorf("%w: definition %s has unknown source %q", ErrLegacyMigrationConflict, definition.ID, definition.Source)
		}
		if err := definition.Validate(); err != nil {
			return LegacyFileExport{}, fmt.Errorf("%w: definition %s: %w", ErrLegacyMigrationConflict, definition.ID, err)
		}
		configs, err := s.q.ListPluginConfigs(ctx, definition.ID)
		if err != nil {
			return LegacyFileExport{}, fmt.Errorf("list configs for %s: %w", definition.ID, err)
		}
		if len(configs) == 0 && definition.Source == SourceBuiltin {
			// Resolve falls back to the shipped default when no row exists. Keep
			// that system-visible resource in the export, including a disabled
			// marker for a builtin whose default is false.
			enabled := definition.DefaultEnabled
			config := Config{ID: "builtin-default/" + definition.ID, PluginID: definition.ID, Scope: ScopeSystem, Enabled: &enabled, Payload: nil, CredentialRefs: json.RawMessage(`{}`), Revision: 1}
			materialized, materializeErr := MaterializePackage(ctx, definition, nil, nil, s.builtinSkillReader)
			if materializeErr != nil {
				return LegacyFileExport{}, fmt.Errorf("materialize builtin %s: %w", definition.ID, materializeErr)
			}
			files := make(map[string]ResourceFile, len(materialized.Files))
			for _, file := range materialized.Files {
				files[file.Path] = ResourceFile{Data: bytes.Clone(file.Data), Mode: file.Mode}
			}
			state, stateErr := ensureOwner(config.Scope, "", "")
			if stateErr != nil {
				return LegacyFileExport{}, stateErr
			}
			if !enabled {
				state.settings.Forbidden = append(state.settings.Forbidden, resourceName(ResourcePlugin, definition.ID))
			}
			key := ResourceKey{Scope: ScopeSystem, Kind: ResourcePlugin, Name: definition.ID}
			result.Entries = append(result.Entries, LegacyFileExportEntry{Key: key, Files: files, SourceDigest: legacySourceDigest(definition, config, "")})
			applyToolPolicies(state, toolPolicies[legacyOwnerID(ScopeSystem, "", "")], definition.ID, "", key)
			continue
		}
		for _, configRow := range configs {
			config := fromSQLConfig(configRow)
			if err := config.Validate(); err != nil {
				return LegacyFileExport{}, fmt.Errorf("%w: config %s: %w", ErrLegacyMigrationConflict, config.ID, err)
			}
			if definition.CreatorUserID != "" && (config.Scope == ScopeSystem || config.Scope == ScopeSystemAgent) {
				return LegacyFileExport{}, fmt.Errorf("%w: private definition %s has system config %s", ErrLegacyMigrationConflict, definition.ID, config.ID)
			}
			if err := loadMCPServerChildren(ctx, s.q, &config); err != nil {
				return LegacyFileExport{}, fmt.Errorf("load MCP children for %s: %w", config.ID, err)
			}
			owner := legacyOwnerID(config.Scope, config.UserID, config.AgentID)
			state, stateErr := ensureOwner(config.Scope, config.UserID, config.AgentID)
			if stateErr != nil {
				return LegacyFileExport{}, stateErr
			}
			payload, payloadErr := decodeMigrationPayload(definition, config)
			if payloadErr != nil {
				return LegacyFileExport{}, payloadErr
			}
			if payload != nil && len(payload.MCPServers) != 0 {
				if err := validateLegacyMCPChildren(config, payload); err != nil {
					return LegacyFileExport{}, err
				}
			}
			if payload != nil && payload.Origin == "remote_mcp" {
				remoteConfig := config
				if len(remoteConfig.Payload) == 0 {
					// Remote configs own child membership. Reconstruct only the
					// formal selector map, rather than passing the definition
					// envelope as config input or selecting no servers.
					selected := make(map[string]map[string]any, len(config.MCPServers))
					for _, child := range config.MCPServers {
						selected[child.ServerKey] = map[string]any{}
					}
					remoteConfig.Payload, _ = json.Marshal(map[string]any{"mcp_servers": selected})
				}
				mcp, mcpErr := MaterializeRemoteMCP(ctx, definition, remoteConfig)
				if mcpErr != nil {
					return LegacyFileExport{}, fmt.Errorf("materialize MCP %s: %w", definition.ID, mcpErr)
				}
				for _, item := range mcp {
					if config.Enabled != nil && !*config.Enabled {
						name := resourceName(ResourceMCP, item.Name)
						if config.Scope == ScopeSystem || config.Scope == ScopeSystemAgent {
							state.settings.Forbidden = append(state.settings.Forbidden, name)
						} else {
							state.settings.Disabled = append(state.settings.Disabled, name)
						}
					}
					result.Entries = append(result.Entries, LegacyFileExportEntry{Key: item.Evidence.NewResource, Files: map[string]ResourceFile{item.File.Path: item.File.ResourceFile}, SourceDigest: legacySourceDigest(definition, config, item.Evidence.OldServerKey)})
					result.MCPMappings = append(result.MCPMappings, item.Evidence)
					applyToolPolicies(state, toolPolicies[owner], definition.ID, item.Evidence.OldServerKey, item.Evidence.NewResource)
				}
				continue
			}
			if config.Enabled != nil && !*config.Enabled {
				name := resourceName(ResourcePlugin, definition.ID)
				if config.Scope == ScopeSystem || config.Scope == ScopeSystemAgent {
					state.settings.Forbidden = append(state.settings.Forbidden, name)
				} else {
					state.settings.Disabled = append(state.settings.Disabled, name)
				}
			}
			var source map[string]ResourceFile
			var sourceDigest string
			if definition.Source == SourceCustom && payload != nil {
				source, sourceDigest, err = s.readLegacyPackageSource(ctx, payload)
				if err != nil {
					return LegacyFileExport{}, err
				}
			}
			materializeConfig := &config
			if len(config.Payload) == 0 {
				materializeConfig = nil
			}
			materialized, materializeErr := materializePackage(ctx, definition, materializeConfig, config.CredentialRefs, source, s.builtinSkillReader)
			if materializeErr != nil {
				return LegacyFileExport{}, fmt.Errorf("materialize package %s: %w", definition.ID, materializeErr)
			}
			key := ResourceKey{Scope: config.Scope, UserID: config.UserID, AgentID: config.AgentID, Kind: ResourcePlugin, Name: definition.ID}
			for _, child := range config.MCPServers {
				result.MCPMappings = append(result.MCPMappings, MCPMigrationEvidence{OldConfigID: config.ID, OldServerKey: child.ServerKey, NewResource: key, OldCredential: credentialLocatorName(config.CredentialRefs, child.ServerKey)})
				applyToolPolicies(state, toolPolicies[owner], definition.ID, child.ServerKey, key)
			}
			if len(materialized.Files) != 0 {
				files := make(map[string]ResourceFile, len(materialized.Files))
				for _, file := range materialized.Files {
					files[file.Path] = ResourceFile{Data: bytes.Clone(file.Data), Mode: file.Mode}
				}
				result.Entries = append(result.Entries, LegacyFileExportEntry{Key: key, Files: files, SourceDigest: legacySourceDigest(definition, config, sourceDigest)})
				applyToolPolicies(state, toolPolicies[owner], definition.ID, "", key)
			}
		}
	}
	for owner, policies := range toolPolicies {
		if _, exists := owners[owner]; exists {
			continue
		}
		parts := strings.Split(owner, "\x00")
		_, stateErr := ensureOwner(Scope(parts[0]), parts[1], parts[2])
		if stateErr != nil {
			return LegacyFileExport{}, stateErr
		}
		if len(policies) != 0 {
			return LegacyFileExport{}, fmt.Errorf("%w: tool policy has no matching plugin config", ErrLegacyMigrationConflict)
		}
	}
	for owner, state := range owners {
		canonicalizeSettings(&state.settings)
		parts := strings.Split(owner, "\x00")
		result.Settings = append(result.Settings, LegacySettingsExport{Scope: Scope(parts[0]), UserID: parts[1], AgentID: parts[2], Settings: state.settings, SourceDigest: state.digest})
	}
	slices.SortFunc(result.Entries, func(a, b LegacyFileExportEntry) int { return strings.Compare(a.Key.ID(), b.Key.ID()) })
	slices.SortFunc(result.Settings, func(a, b LegacySettingsExport) int {
		return strings.Compare(legacyOwnerID(a.Scope, a.UserID, a.AgentID), legacyOwnerID(b.Scope, b.UserID, b.AgentID))
	})
	return result, nil
}

// PublishLegacyFileExport applies an export after a complete preflight. A
// retry is accepted only when each existing target already equals the export.
func (s *LegacyService) PublishLegacyFileExport(ctx context.Context, store *ResourceStore, export LegacyFileExport) error {
	if store == nil {
		return ErrLegacyMigrationConflict
	}
	if err := contextError(ctx); err != nil {
		return err
	}
	settings := make([]LegacySettingsExport, len(export.Settings))
	copy(settings, export.Settings)
	for _, entry := range export.Entries {
		if err := preflightEntry(ctx, store, entry); err != nil {
			return err
		}
	}
	finalSettings := make([]LegacySettingsExport, 0, len(settings))
	for _, setting := range settings {
		final, err := preflightSettings(ctx, store, setting)
		if err != nil {
			return err
		}
		finalSettings = append(finalSettings, final)
	}
	for _, entry := range export.Entries {
		if entry.Key.Kind == ResourceMCP {
			file, ok := entry.Files[mcpPath(entry.Key.Name)]
			if !ok {
				return ErrLegacyFileExportConflict
			}
			if _, err := store.WriteMCP(ctx, entry.Key, "", file.Data); err != nil {
				if !errors.Is(err, ErrConflict) {
					return err
				}
				if err := verifyEntry(ctx, store, entry); err != nil {
					return err
				}
			}
		} else {
			if _, err := store.CreatePlugin(ctx, entry.Key, entry.Files); err != nil {
				if !errors.Is(err, ErrConflict) {
					return err
				}
				if err := verifyEntry(ctx, store, entry); err != nil {
					return err
				}
			}
		}
	}
	for _, setting := range finalSettings {
		if err := writeLegacySettings(ctx, store, setting); err != nil {
			return err
		}
	}
	return nil
}

// VerifyLegacyFileExport checks that all targets equal the prepared export.
func (s *LegacyService) VerifyLegacyFileExport(ctx context.Context, store *ResourceStore, export LegacyFileExport) error {
	for _, entry := range export.Entries {
		if err := verifyEntry(ctx, store, entry); err != nil {
			return err
		}
	}
	for _, setting := range export.Settings {
		current, _, err := store.ReadSettings(ctx, setting.Scope, setting.UserID, setting.AgentID)
		if err != nil || !settingsEqual(current, setting.Settings) {
			if err != nil {
				return err
			}
			return ErrLegacyFileExportConflict
		}
	}
	return nil
}

func preflightEntry(ctx context.Context, store *ResourceStore, entry LegacyFileExportEntry) error {
	if entry.Key.Kind == ResourceMCP {
		data, _, err := store.ReadMCP(ctx, entry.Key)
		want := entry.Files[mcpPath(entry.Key.Name)].Data
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil || !bytes.Equal(data, want) {
			return ErrLegacyFileExportConflict
		}
		return nil
	}
	existing, err := store.Get(ctx, entry.Key)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if existing.Content == nil {
		return ErrLegacyFileExportConflict
	}
	files, err := resourceTree(existing.Content)
	if err != nil || PreparedFileDigest(files) != PreparedFileDigest(entry.Files) {
		return ErrLegacyFileExportConflict
	}
	return nil
}

func verifyEntry(ctx context.Context, store *ResourceStore, entry LegacyFileExportEntry) error {
	if entry.Key.Kind == ResourceMCP {
		data, _, err := store.ReadMCP(ctx, entry.Key)
		if err != nil {
			return err
		}
		want := entry.Files[mcpPath(entry.Key.Name)].Data
		if !bytes.Equal(data, want) {
			return ErrLegacyFileExportConflict
		}
		return nil
	}
	existing, err := store.Get(ctx, entry.Key)
	if err != nil {
		return err
	}
	if existing.Content == nil {
		return ErrLegacyFileExportConflict
	}
	files, err := resourceTree(existing.Content)
	if err != nil || PreparedFileDigest(files) != PreparedFileDigest(entry.Files) {
		return ErrLegacyFileExportConflict
	}
	return nil
}

func preflightSettings(ctx context.Context, store *ResourceStore, setting LegacySettingsExport) (LegacySettingsExport, error) {
	current, digest, err := store.ReadSettings(ctx, setting.Scope, setting.UserID, setting.AgentID)
	if err != nil {
		return LegacySettingsExport{}, err
	}
	final := setting
	final.SourceDigest = digest
	if settingsEqual(current, setting.Settings) {
		return final, nil
	}
	// The prepared source digest authorizes the initial write. A target that is
	// already the exact desired projection is an idempotent retry. Any other
	// drift would make the signed write set ambiguous.
	if digest != setting.SourceDigest {
		return LegacySettingsExport{}, ErrLegacyFileExportConflict
	}
	return final, nil
}

func writeLegacySettings(ctx context.Context, store *ResourceStore, setting LegacySettingsExport) error {
	key := ResourceKey{Scope: setting.Scope, UserID: setting.UserID, AgentID: setting.AgentID, Kind: ResourcePlugin, Name: "settings"}
	root, err := store.open(ctx, key, home.RootReadWrite)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	current, digest, err := readSettingsState(ctx, root)
	if err != nil {
		return err
	}
	if settingsEqual(current, setting.Settings) {
		return nil
	}
	if digest != setting.SourceDigest {
		return ErrConflict
	}
	canonicalizeSettings(&setting.Settings)
	data, err := json.Marshal(setting.Settings)
	if err != nil {
		return err
	}
	if settingsEmpty(setting.Settings) {
		if digest != "" {
			return root.Remove(ctx, "settings.json", home.RemoveOptions{})
		}
		return nil
	}
	return root.Upload(ctx, "settings.json", bytes.NewReader(data), home.WriteOptions{Mode: 0o600, MaxBytes: int64(len(data)) + 1, Sync: true})
}

func decodeMigrationPayload(definition Definition, config Config) (*ResourcePayload, error) {
	raw := definition.Spec
	var err error
	if len(config.Payload) != 0 {
		raw, err = MergeDefinitionConfig(definition.Spec, config.Payload)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: merge config %s: %w", ErrLegacyMigrationConflict, config.ID, err)
	}
	payload, err := DecodeResourcePayload(raw, "legacy config "+config.ID)
	if err != nil {
		return nil, fmt.Errorf("%w: decode config %s: %w", ErrLegacyMigrationConflict, config.ID, err)
	}
	return &payload, nil
}

func validateLegacyMCPChildren(config Config, payload *ResourcePayload) error {
	children := make(map[string]struct{}, len(config.MCPServers))
	for _, child := range config.MCPServers {
		if child.ServerKey == "" {
			return fmt.Errorf("%w: config %s has an empty MCP child key", ErrLegacyMigrationConflict, config.ID)
		}
		children[child.ServerKey] = struct{}{}
	}
	for key := range payload.MCPServers {
		if _, ok := children[key]; !ok {
			return fmt.Errorf("%w: config %s MCP server %q has no durable child", ErrLegacyMigrationConflict, config.ID, key)
		}
	}
	if len(children) != len(payload.MCPServers) {
		return fmt.Errorf("%w: config %s MCP child set differs from declaration", ErrLegacyMigrationConflict, config.ID)
	}
	return nil
}

func (s *LegacyService) readLegacyPackageSource(ctx context.Context, payload *ResourcePayload) (map[string]ResourceFile, string, error) {
	if s.contentStore == nil || payload == nil || payload.Content == nil {
		return nil, "", fmt.Errorf("%w: custom package has no content reference", ErrLegacyMigrationConflict)
	}
	digest := strings.TrimPrefix(payload.Content.Digest, "sha256:")
	if !validStoreDigest(digest) {
		return nil, "", fmt.Errorf("%w: invalid custom package digest", ErrLegacyMigrationConflict)
	}
	s.contentStore.mu.Lock()
	defer s.contentStore.mu.Unlock()
	root := path.Join(s.contentStore.root, digest)
	actual, err := agentpackage.DirectoryDigest(root)
	if err != nil || actual != "sha256:"+digest {
		return nil, "", fmt.Errorf("%w: custom package digest changed", ErrLegacyMigrationConflict)
	}
	disk, err := os.OpenRoot(root)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = disk.Close() }()
	files := map[string]ResourceFile{}
	err = fs.WalkDir(disk.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return errors.New("custom package contains symlink")
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return errors.New("custom package contains non-regular file")
		}
		data, err := fs.ReadFile(disk.FS(), name)
		if err != nil {
			return err
		}
		files[path.Clean(name)] = ResourceFile{Data: data, Mode: info.Mode().Perm()}
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	return files, "sha256:" + digest, nil
}

// legacyToolPolicies reads the durable plugin tool policy rows. A payload field
// is not an authority for tool enablement and is intentionally ignored.
func (s *LegacyService) legacyToolPolicies(ctx context.Context) (map[string]map[string][]string, error) {
	result := map[string]map[string][]string{}
	if s.db == nil {
		return result, nil
	}
	rows, err := s.db.Query(ctx, `
		SELECT plugin_id, server_key, local_tool_name, scope, user_id, agent_id, enabled
		FROM tool_override o
		JOIN plugin_definition d ON d.id = o.plugin_id
		WHERE o.plugin_id IS NOT NULL AND d.retired_at IS NULL
		ORDER BY o.plugin_id, o.server_key, o.local_tool_name, o.scope, o.user_id, o.agent_id`)
	if err != nil {
		return nil, fmt.Errorf("list plugin tool policies: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var pluginID, serverKey, localTool pgtype.Text
		var scope string
		var userID, agentID pgtype.Text
		var enabled bool
		if err := rows.Scan(&pluginID, &serverKey, &localTool, &scope, &userID, &agentID, &enabled); err != nil {
			return nil, err
		}
		if enabled || !pluginID.Valid || !localTool.Valid || localTool.String == "" {
			continue
		}
		owner := legacyOwnerID(Scope(scope), textValue(userID), textValue(agentID))
		if !validScope(Scope(scope)) || !ownerMatches(Scope(scope), textValue(userID), textValue(agentID)) {
			return nil, fmt.Errorf("%w: invalid tool policy owner", ErrLegacyMigrationConflict)
		}
		if result[owner] == nil {
			result[owner] = map[string][]string{}
		}
		target := resourceName(ResourcePlugin, pluginID.String)
		if serverKey.Valid && serverKey.String != "" {
			target = "mcp-server:" + pluginID.String + ":" + serverKey.String
		}
		result[owner][target] = append(result[owner][target], localTool.String)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, targets := range result {
		for target, tools := range targets {
			slices.Sort(tools)
			targets[target] = slices.Compact(tools)
		}
	}
	return result, nil
}

func applyToolPolicies(state *legacyOwnerState, policies map[string][]string, pluginID, serverKey string, key ResourceKey) {
	if state == nil || len(policies) == 0 {
		return
	}
	if state.settings.DisabledTools == nil {
		state.settings.DisabledTools = map[string][]string{}
	}
	target := resourceName(key.Kind, key.Name)
	if serverKey != "" {
		if tools := policies["mcp-server:"+pluginID+":"+serverKey]; len(tools) != 0 {
			state.settings.DisabledTools[target] = append(state.settings.DisabledTools[target], tools...)
		}
	} else if tools := policies[resourceName(ResourcePlugin, pluginID)]; len(tools) != 0 {
		state.settings.DisabledTools[target] = append(state.settings.DisabledTools[target], tools...)
	}
}

func legacySourceDigest(definition Definition, config Config, extra string) string {
	object := struct {
		DefinitionID string           `json:"definition_id"`
		Revision     int64            `json:"revision"`
		Spec         json.RawMessage  `json:"spec"`
		ConfigID     string           `json:"config_id"`
		Scope        Scope            `json:"scope"`
		UserID       string           `json:"user_id"`
		AgentID      string           `json:"agent_id"`
		Enabled      *bool            `json:"enabled"`
		Payload      json.RawMessage  `json:"payload"`
		Credential   json.RawMessage  `json:"credential_refs"`
		MCPChildren  []MCPServerChild `json:"mcp_children"`
		Extra        string           `json:"extra"`
	}{definition.ID, definition.Revision, definition.Spec, config.ID, config.Scope, config.UserID, config.AgentID, config.Enabled, config.Payload, config.CredentialRefs, config.MCPServers, extra}
	data, _ := json.Marshal(object)
	digest := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func legacyOwnerID(scope Scope, userID, agentID string) string {
	return string(scope) + "\x00" + userID + "\x00" + agentID
}

func canonicalSettings(settings ResourceSettings) ResourceSettings {
	result := settings
	result.Disabled = slices.Clone(result.Disabled)
	result.Forbidden = slices.Clone(result.Forbidden)
	result.DisabledTools = maps.Clone(result.DisabledTools)
	canonicalizeSettings(&result)
	return result
}

func settingsEqual(a, b ResourceSettings) bool {
	aa, _ := json.Marshal(canonicalSettings(a))
	bb, _ := json.Marshal(canonicalSettings(b))
	return bytes.Equal(aa, bb)
}
