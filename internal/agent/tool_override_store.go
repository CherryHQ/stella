package agent

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/CherryHQ/stella/internal/sessionexecution"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	agentruntime "github.com/CherryHQ/stella/internal/agent/runtime"
	"github.com/CherryHQ/stella/internal/agent/settingspolicy"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/platform/config"
	internalplugin "github.com/CherryHQ/stella/internal/plugin"
	"github.com/CherryHQ/stella/pkg/db/pgnull"
	sqlc "github.com/CherryHQ/stella/pkg/db/sqlc"
)

// ToolOverrideAbsentVersion is the only version accepted when creating an exact
// override row. It makes a first write conditional without inventing a row to
// version before the user has chosen an override.
const ToolOverrideAbsentVersion = "absent"

// ToolOverrideStore reads persisted tool-visibility overrides for an
// agent+user context. Its Fetch method satisfies ToolOverrideFetcher.
type ToolOverrideStore struct {
	db    *pgxpool.Pool
	q     *sqlc.Queries
	files *internalplugin.FileService
}

// NewToolOverrideStore builds a ToolOverrideStore over the given pool.
func NewToolOverrideStore(db *pgxpool.Pool, files ...*internalplugin.FileService) *ToolOverrideStore {
	store := &ToolOverrideStore{db: db, q: sqlc.New(db)}
	if len(files) > 0 {
		store.files = files[0]
	}
	return store
}

// Fetch returns the tool overrides that apply to the given user+agent pair.
func (s *ToolOverrideStore) Fetch(ctx context.Context, userID, agentID string) ([]ToolOverride, error) {
	rows, err := s.q.ListToolOverridesForAgentContext(ctx, sqlc.ListToolOverridesForAgentContextParams{
		UserID: pgnull.Text(userID), AgentID: pgnull.Text(agentID),
	})
	if err != nil {
		return nil, err
	}
	out := make([]ToolOverride, 0, len(rows))
	for _, row := range rows {
		// Legacy plugin rows are migration input only. Skip them before decoding
		// their old identity shape so one malformed retired row cannot block the
		// file-backed runtime policy snapshot.
		if row.PluginID.Valid {
			continue
		}
		identity, err := persistedToolIdentity(row)
		if err != nil {
			return nil, fmt.Errorf("tool override: invalid persisted identity: %w", err)
		}
		// Database plugin identities are migration input only. File-backed
		// resources are the runtime authority after cutover, so stale rows must
		// not shadow or invalidate the effective file snapshot.
		if identity.isPlugin() {
			continue
		}
		if identity.CoreToolName != "" && isSettingsManagedTool(identity.CoreToolName) {
			continue
		}
		override := ToolOverride{Identity: identity, Scope: row.Scope, Enabled: row.Enabled}
		out = append(out, override)
	}
	fileRows, err := s.fileOverrides(ctx, userID, agentID)
	if err != nil {
		return nil, err
	}
	out = append(out, fileRows...)
	return out, nil
}

// ToolOverrideWrite is the durable owner+scope key plus the desired enabled state
// for one tool visibility override.
type ToolOverrideWrite struct {
	Identity ToolIdentity
	Scope    string
	UserID   string
	AgentID  string
	Enabled  bool
}

// ToolOverrideKey identifies one override row for clearing.
type ToolOverrideKey struct {
	Identity ToolIdentity
	Scope    string
	UserID   string
	AgentID  string
}

// ToolOverrideVersion is the safe exact-row projection used by management tools.
type ToolOverrideVersion struct {
	Identity *ToolIdentity `json:"identity,omitempty"`
	ToolName string        `json:"tool_name"`
	Scope    string        `json:"scope"`
	Enabled  bool          `json:"enabled"`
	Version  string        `json:"version"`
	Present  bool          `json:"present"`
	// Family is set only for MCP tools ("mcp:<server>"); generated tools carry
	// their family in the toolmeta registry and the profile tools endpoint.
	Family string `json:"family,omitempty"`
}

// Get returns one exact owner-bound override. Missing rows receive the stable
// absent sentinel so a caller can conditionally create rather than race an
// unguarded upsert.
// ListVersions returns every exact user+agent override in one query. Missing
// tools are represented by callers with ToolOverrideAbsentVersion.
func (s *ToolOverrideStore) ListVersions(ctx context.Context, userID, agentID string) (map[string]ToolOverrideVersion, error) {
	rows, err := s.q.ListToolOverridesForAgentContext(ctx, sqlc.ListToolOverridesForAgentContextParams{
		UserID: pgnull.Text(userID), AgentID: pgnull.Text(agentID),
	})
	if err != nil {
		return nil, err
	}
	out := make(map[string]ToolOverrideVersion, len(rows))
	for _, row := range rows {
		if row.PluginID.Valid {
			continue
		}
		identity, err := persistedToolIdentity(row)
		if err != nil {
			return nil, fmt.Errorf("tool override: invalid persisted identity: %w", err)
		}
		if identity.isPlugin() || row.Scope != ToolOverrideScopeUserAgent || (identity.CoreToolName != "" && isSettingsManagedTool(identity.CoreToolName)) {
			continue
		}
		out[toolOverrideVersionKey(identity)] = overrideVersion(row)
	}
	fileRows, err := s.fileOverrides(ctx, userID, agentID)
	if err != nil {
		return nil, err
	}
	for _, row := range fileRows {
		if row.Scope != ToolOverrideScopeUserAgent {
			continue
		}
		version, err := s.getFile(ctx, row.Identity, row.Scope, userID, agentID)
		if err != nil {
			return nil, err
		}
		out[toolOverrideVersionKey(row.Identity)] = ToolOverrideVersion{
			Identity: &row.Identity, ToolName: "", Scope: row.Scope, Enabled: version.Enabled,
			Present: true, Version: version.Version,
		}
	}
	return out, nil
}

func (s *ToolOverrideStore) Get(ctx context.Context, k ToolOverrideKey) (ToolOverrideVersion, error) {
	if !isOverrideScope(k.Scope) {
		return ToolOverrideVersion{}, fmt.Errorf("tool override: invalid scope %q", k.Scope)
	}
	identity, err := k.toolIdentity()
	if err != nil {
		return ToolOverrideVersion{}, err
	}
	if isFileToolIdentity(identity) {
		return s.getFile(ctx, identity, k.Scope, k.UserID, k.AgentID)
	}
	row, err := s.q.GetToolOverrideByIdentity(ctx, identityParams(identity, k.Scope, k.UserID, k.AgentID))
	if errors.Is(err, pgx.ErrNoRows) {
		version := ToolOverrideVersion{ToolName: identity.CoreToolName, Scope: k.Scope, Version: ToolOverrideAbsentVersion}
		if identity.isPlugin() {
			version.Identity = &identity
		}
		return version, nil
	}
	if err != nil {
		return ToolOverrideVersion{}, err
	}
	return overrideVersion(row), nil
}

// Set upserts a tool visibility override for existing HTTP callers, which keep
// their historical unconditional-write contract.
func (s *ToolOverrideStore) Set(ctx context.Context, w ToolOverrideWrite) error {
	if !isOverrideScope(w.Scope) {
		return fmt.Errorf("tool override: invalid scope %q", w.Scope)
	}
	identity, err := w.toolIdentity()
	if err != nil {
		return err
	}
	if isFileToolIdentity(identity) {
		_, err := s.setFile(ctx, identity, w.Scope, w.UserID, w.AgentID, w.Enabled, "")
		return err
	}
	if identity.isPlugin() {
		_, err = sessionexecution.Write(ctx, s.db, func(ctx context.Context, q *sqlc.Queries) (sqlc.ToolOverride, error) {
			return q.UpsertPluginToolOverride(ctx, sqlc.UpsertPluginToolOverrideParams{
				PluginID: pgnull.Text(identity.PluginID), ServerKey: pgnull.Text(identity.ServerKey), LocalToolName: pgnull.Text(identity.LocalToolName), Scope: w.Scope,
				UserID: pgnull.Text(w.UserID), AgentID: pgnull.Text(w.AgentID), Enabled: w.Enabled,
			})
		})
	} else {
		_, err = sessionexecution.Write(ctx, s.db, func(ctx context.Context, q *sqlc.Queries) (sqlc.ToolOverride, error) {
			return q.UpsertCoreToolOverride(ctx, sqlc.UpsertCoreToolOverrideParams{
				ToolName: pgnull.Text(identity.CoreToolName), Scope: w.Scope, UserID: pgnull.Text(w.UserID), AgentID: pgnull.Text(w.AgentID), Enabled: w.Enabled,
			})
		})
	}
	return err
}

// SetIfVersion updates an existing row or creates an absent row only when its
// version still matches. A zero-row conditional write is a conflict, never a
// silent retry or an unconditional upsert.
func (s *ToolOverrideStore) SetIfVersion(ctx context.Context, w ToolOverrideWrite, expected string) (ToolOverrideVersion, error) {
	if !isOverrideScope(w.Scope) {
		return ToolOverrideVersion{}, fmt.Errorf("tool override: invalid scope %q", w.Scope)
	}
	identity, err := w.toolIdentity()
	if err != nil {
		return ToolOverrideVersion{}, err
	}
	if isFileToolIdentity(identity) {
		return s.setFile(ctx, identity, w.Scope, w.UserID, w.AgentID, w.Enabled, expected)
	}
	if expected == ToolOverrideAbsentVersion {
		var row sqlc.ToolOverride
		if identity.isPlugin() {
			row, err = sessionexecution.Write(ctx, s.db, func(ctx context.Context, q *sqlc.Queries) (sqlc.ToolOverride, error) {
				return q.InsertPluginToolOverrideIfAbsent(ctx, sqlc.InsertPluginToolOverrideIfAbsentParams{
					PluginID: pgnull.Text(identity.PluginID), ServerKey: pgnull.Text(identity.ServerKey), LocalToolName: pgnull.Text(identity.LocalToolName), Scope: w.Scope,
					UserID: pgnull.Text(w.UserID), AgentID: pgnull.Text(w.AgentID), Enabled: w.Enabled,
				})
			})
		} else {
			row, err = sessionexecution.Write(ctx, s.db, func(ctx context.Context, q *sqlc.Queries) (sqlc.ToolOverride, error) {
				return q.InsertCoreToolOverrideIfAbsent(ctx, sqlc.InsertCoreToolOverrideIfAbsentParams{
					ToolName: pgnull.Text(identity.CoreToolName), Scope: w.Scope, UserID: pgnull.Text(w.UserID), AgentID: pgnull.Text(w.AgentID), Enabled: w.Enabled,
				})
			})
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return ToolOverrideVersion{}, config.ErrAgentVersionConflict
		}
		if err != nil {
			return ToolOverrideVersion{}, err
		}
		return overrideVersion(row), nil
	}
	expectedAt, err := parseOverrideVersion(expected)
	if err != nil {
		return ToolOverrideVersion{}, config.ErrAgentVersionConflict
	}
	var row sqlc.ToolOverride
	if identity.isPlugin() {
		row, err = sessionexecution.Write(ctx, s.db, func(ctx context.Context, q *sqlc.Queries) (sqlc.ToolOverride, error) {
			return q.UpdatePluginToolOverrideIfVersion(ctx, sqlc.UpdatePluginToolOverrideIfVersionParams{
				Enabled: w.Enabled, PluginID: pgnull.Text(identity.PluginID), ServerKey: pgnull.Text(identity.ServerKey), LocalToolName: pgnull.Text(identity.LocalToolName), Scope: w.Scope,
				UserID: pgnull.Text(w.UserID), AgentID: pgnull.Text(w.AgentID), ExpectedUpdatedAt: expectedAt,
			})
		})
	} else {
		row, err = sessionexecution.Write(ctx, s.db, func(ctx context.Context, q *sqlc.Queries) (sqlc.ToolOverride, error) {
			return q.UpdateCoreToolOverrideIfVersion(ctx, sqlc.UpdateCoreToolOverrideIfVersionParams{
				Enabled: w.Enabled, ToolName: pgnull.Text(identity.CoreToolName), Scope: w.Scope, UserID: pgnull.Text(w.UserID), AgentID: pgnull.Text(w.AgentID), ExpectedUpdatedAt: expectedAt,
			})
		})
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ToolOverrideVersion{}, config.ErrAgentVersionConflict
	}
	if err != nil {
		return ToolOverrideVersion{}, err
	}
	return overrideVersion(row), nil
}

// Clear deletes a tool visibility override if present for existing HTTP callers.
func (s *ToolOverrideStore) Clear(ctx context.Context, k ToolOverrideKey) error {
	if !isOverrideScope(k.Scope) {
		return fmt.Errorf("tool override: invalid scope %q", k.Scope)
	}
	identity, err := k.toolIdentity()
	if err != nil {
		return err
	}
	if isFileToolIdentity(identity) {
		_, err := s.clearFile(ctx, identity, k.Scope, k.UserID, k.AgentID, "")
		return err
	}
	if identity.isPlugin() {
		return sessionexecution.Exec(ctx, s.db, func(ctx context.Context, q *sqlc.Queries) error {
			return q.DeletePluginToolOverride(ctx, sqlc.DeletePluginToolOverrideParams{
				PluginID: pgnull.Text(identity.PluginID), ServerKey: pgnull.Text(identity.ServerKey), LocalToolName: pgnull.Text(identity.LocalToolName), Scope: k.Scope,
				UserID: pgnull.Text(k.UserID), AgentID: pgnull.Text(k.AgentID),
			})
		})
	}
	return sessionexecution.Exec(ctx, s.db, func(ctx context.Context, q *sqlc.Queries) error {
		return q.DeleteCoreToolOverride(ctx, sqlc.DeleteCoreToolOverrideParams{
			ToolName: pgnull.Text(identity.CoreToolName), Scope: k.Scope, UserID: pgnull.Text(k.UserID), AgentID: pgnull.Text(k.AgentID),
		})
	})
}

// ClearIfVersion deletes only an existing row at the version returned by Get.
func (s *ToolOverrideStore) ClearIfVersion(ctx context.Context, k ToolOverrideKey, expected string) error {
	if !isOverrideScope(k.Scope) || expected == ToolOverrideAbsentVersion {
		return config.ErrAgentVersionConflict
	}
	identity, err := k.toolIdentity()
	if err != nil {
		return err
	}
	if isFileToolIdentity(identity) {
		_, err := s.clearFile(ctx, identity, k.Scope, k.UserID, k.AgentID, expected)
		return err
	}
	expectedAt, err := parseOverrideVersion(expected)
	if err != nil {
		return config.ErrAgentVersionConflict
	}
	if identity.isPlugin() {
		_, err = sessionexecution.Write(ctx, s.db, func(ctx context.Context, q *sqlc.Queries) (sqlc.ToolOverride, error) {
			return q.DeletePluginToolOverrideIfVersion(ctx, sqlc.DeletePluginToolOverrideIfVersionParams{
				PluginID: pgnull.Text(identity.PluginID), ServerKey: pgnull.Text(identity.ServerKey), LocalToolName: pgnull.Text(identity.LocalToolName), Scope: k.Scope,
				UserID: pgnull.Text(k.UserID), AgentID: pgnull.Text(k.AgentID), ExpectedUpdatedAt: expectedAt,
			})
		})
	} else {
		_, err = sessionexecution.Write(ctx, s.db, func(ctx context.Context, q *sqlc.Queries) (sqlc.ToolOverride, error) {
			return q.DeleteCoreToolOverrideIfVersion(ctx, sqlc.DeleteCoreToolOverrideIfVersionParams{
				ToolName: pgnull.Text(identity.CoreToolName), Scope: k.Scope, UserID: pgnull.Text(k.UserID), AgentID: pgnull.Text(k.AgentID), ExpectedUpdatedAt: expectedAt,
			})
		})
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return config.ErrAgentVersionConflict
	}
	return err
}

func identityParams(identity ToolIdentity, scope, userID, agentID string) sqlc.GetToolOverrideByIdentityParams {
	return sqlc.GetToolOverrideByIdentityParams{
		ToolName: pgnull.Text(identity.CoreToolName), PluginID: pgnull.Text(identity.PluginID), ServerKey: pgnull.Text(identity.ServerKey), LocalToolName: pgnull.Text(identity.LocalToolName),
		Scope: scope, UserID: pgnull.Text(userID), AgentID: pgnull.Text(agentID),
	}
}

func overrideVersion(row sqlc.ToolOverride) ToolOverrideVersion {
	identity, _ := persistedToolIdentity(row)
	toolName := ""
	if row.ToolName.Valid {
		toolName = row.ToolName.String
	}
	version := ToolOverrideVersion{ToolName: toolName, Scope: row.Scope, Enabled: row.Enabled, Present: true, Version: row.UpdatedAt.UTC().Format(time.RFC3339Nano)}
	if identity.isPlugin() {
		version.Identity = &identity
	}
	return version
}

// toolOverrideVersionKey keeps the management projection collision-free while
// retaining the historical core-name key for core tools. Plugin exported names
// are display values and cannot identify a policy row on their own.
func toolOverrideVersionKey(identity ToolIdentity) string {
	if identity.isPlugin() {
		return "plugin:" + identity.PluginID + "\x00" + identity.ServerKey + "\x00" + identity.LocalToolName
	}
	return identity.CoreToolName
}

func (w ToolOverrideWrite) toolIdentity() (ToolIdentity, error) {
	if err := w.Identity.Validate(); err != nil {
		return ToolIdentity{}, fmt.Errorf("tool override: %w", err)
	}
	return w.Identity, nil
}

func (k ToolOverrideKey) toolIdentity() (ToolIdentity, error) {
	if err := k.Identity.Validate(); err != nil {
		return ToolIdentity{}, fmt.Errorf("tool override: %w", err)
	}
	return k.Identity, nil
}

func persistedToolIdentity(row sqlc.ToolOverride) (ToolIdentity, error) {
	if row.PluginID.Valid || row.ServerKey.Valid || row.LocalToolName.Valid {
		identity := ToolIdentity{PluginID: row.PluginID.String, ServerKey: row.ServerKey.String, LocalToolName: row.LocalToolName.String}
		if err := identity.Validate(); err != nil {
			return ToolIdentity{}, err
		}
		return identity, nil
	}
	if !row.ToolName.Valid || row.ToolName.String == "" {
		return ToolIdentity{}, fmt.Errorf("core tool_name is required")
	}
	return ToolIdentity{CoreToolName: row.ToolName.String}, nil
}

func parseOverrideVersion(version string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, version)
}

func isSettingsManagedTool(name string) bool {
	_, ok := settingspolicy.Lookup(name)
	return ok
}

func isOverrideScope(scope string) bool {
	switch scope {
	case ToolOverrideScopeSystem, ToolOverrideScopeSystemAgent, ToolOverrideScopeUser, ToolOverrideScopeUserAgent:
		return true
	default:
		return false
	}
}

func isFileToolIdentity(identity ToolIdentity) bool {
	return identity.isPlugin() && strings.HasPrefix(identity.PluginID, "file:")
}

// fileOverrides projects every effective settings layer into runner rows. The
// file provider already filters these tools from the executable snapshot; this
// projection keeps the runner's policy view consistent for a tool that was
// catalogued before the next file capture, including administrator denies.
func (s *ToolOverrideStore) fileOverrides(ctx context.Context, userID, agentID string) ([]ToolOverride, error) {
	if s == nil || s.files == nil || userID == "" || agentID == "" {
		return nil, nil
	}
	authority, ok := authz.AuthorityFromContext(ctx)
	if !ok || !authority.Valid() {
		return nil, authz.ErrUnauthenticated
	}
	if prepared, ok := agentruntime.PreparedPluginContext(ctx); ok {
		if !prepared.IsFileBased() {
			return nil, errors.New("tool override: prepared context is not file-backed")
		}
		return fileResourceOverrides(prepared.FileResources()), nil
	}
	if authority.Kind() == authz.ActorGroupAgent {
		// Group turns deliberately carry the synthetic group owner in RunnerParams,
		// while their Authority has no user identity. Re-project the already
		// captured system/system-agent deny ceiling instead of opening personal
		// settings through FileAccess. Capture also applies the Agent PEP and keeps
		// the group resource boundary intact.
		resources, err := s.files.Capture(ctx, authority, agentID)
		if err != nil {
			return nil, err
		}
		return fileResourceOverrides(resources), nil
	}
	if authority.Kind() != authz.ActorUser && authority.Kind() != authz.ActorAgent {
		return nil, authz.ErrForbidden
	}
	if string(authority.UserID()) != userID {
		return nil, authz.ErrForbidden
	}
	access, err := s.files.Begin(authority)
	if err != nil {
		return nil, err
	}
	resources, err := s.files.ReadSnapshot(ctx, authority, agentID)
	if err != nil {
		return nil, err
	}
	effective := make(map[string]internalplugin.ResourceKey, len(resources))
	for _, resource := range resources {
		effective[string(resource.Key.Kind)+":"+resource.Key.Name] = resource.Key
	}
	var out []ToolOverride
	for _, scope := range []internalplugin.Scope{internalplugin.ScopeSystem, internalplugin.ScopeSystemAgent, internalplugin.ScopeUser, internalplugin.ScopeUserAgent} {
		ownerAgent := ""
		if scope == internalplugin.ScopeSystemAgent || scope == internalplugin.ScopeUserAgent {
			ownerAgent = agentID
		}
		layer, _, err := access.ReadSettings(ctx, scope, ownerAgent)
		if err != nil {
			return nil, err
		}
		for resourceName, rawTools := range layer.DisabledTools {
			resourceKey, ok := effective[resourceName]
			if !ok {
				continue
			}
			for _, raw := range rawTools {
				server, local, ok := splitFileToolRef(raw)
				if !ok {
					continue
				}
				identity := ToolIdentity{PluginID: resourceKey.ID(), ServerKey: server, LocalToolName: local}
				if identity.Validate() != nil {
					continue
				}
				out = append(out, ToolOverride{Identity: identity, Scope: string(scope), Enabled: false})
			}
		}
	}
	return out, nil
}

// fileResourceOverrides projects the effective deny list captured for a
// runtime authority. FileResource.DisabledTools is already composed across the
// visible layers, so this path must not read those layers a second time under a
// different authority.
func fileResourceOverrides(resources []internalplugin.FileResource) []ToolOverride {
	var out []ToolOverride
	for _, resource := range resources {
		for _, raw := range resource.DisabledTools {
			server, local, ok := splitFileToolRef(raw)
			if !ok {
				continue
			}
			identity := ToolIdentity{PluginID: resource.Key.ID(), ServerKey: server, LocalToolName: local}
			if identity.Validate() != nil {
				continue
			}
			// The capture has already restricted the resource to the authority's
			// visible scopes. Preserve the winning admin scope for diagnostics;
			// all entries are denies and therefore remain a ceiling in Resolve.
			out = append(out, ToolOverride{Identity: identity, Scope: string(resource.Key.Scope), Enabled: false})
		}
	}
	return out
}

func filePolicyTarget(identity ToolIdentity, scope, userID, agentID string) (internalplugin.ResourceKey, error) {
	key, err := internalplugin.ParseResourceID(identity.PluginID)
	if err != nil {
		return internalplugin.ResourceKey{}, err
	}
	key.Scope = internalplugin.Scope(scope)
	key.UserID, key.AgentID = "", ""
	switch key.Scope {
	case internalplugin.ScopeSystem:
	case internalplugin.ScopeSystemAgent:
		key.AgentID = agentID
	case internalplugin.ScopeUser:
		key.UserID = userID
	case internalplugin.ScopeUserAgent:
		key.UserID, key.AgentID = userID, agentID
	default:
		return internalplugin.ResourceKey{}, fmt.Errorf("tool override: invalid file scope %q", scope)
	}
	if key.ID() == "" {
		return internalplugin.ResourceKey{}, fmt.Errorf("tool override: invalid file resource identity")
	}
	return key, nil
}

func fileToolRef(identity ToolIdentity) string {
	return url.PathEscape(identity.ServerKey) + "/" + url.PathEscape(identity.LocalToolName)
}

func splitFileToolRef(raw string) (string, string, bool) {
	separator := strings.IndexByte(raw, '/')
	if separator <= 0 || separator == len(raw)-1 || strings.IndexByte(raw[separator+1:], '/') >= 0 {
		return "", "", false
	}
	server, err := url.PathUnescape(raw[:separator])
	if err != nil || url.PathEscape(server) != raw[:separator] {
		return "", "", false
	}
	local, err := url.PathUnescape(raw[separator+1:])
	if err != nil || url.PathEscape(local) != raw[separator+1:] || server == "" || local == "" {
		return "", "", false
	}
	return server, local, true
}

func (s *ToolOverrideStore) getFile(ctx context.Context, identity ToolIdentity, scope, userID, agentID string) (ToolOverrideVersion, error) {
	if s == nil || s.files == nil {
		return ToolOverrideVersion{}, fmt.Errorf("tool override: file policy unavailable")
	}
	key, err := filePolicyTarget(identity, scope, userID, agentID)
	if err != nil {
		return ToolOverrideVersion{}, err
	}
	authority, err := fileAuthority(ctx, userID, agentID)
	if err != nil {
		return ToolOverrideVersion{}, err
	}
	access, err := s.files.Begin(authority)
	if err != nil {
		return ToolOverrideVersion{}, err
	}
	if err := access.AuthorizeKey(ctx, key, false); err != nil {
		return ToolOverrideVersion{}, err
	}
	settings, digest, err := access.ReadSettings(ctx, key.Scope, key.AgentID)
	if err != nil {
		return ToolOverrideVersion{}, err
	}
	blocked := slices.Contains(settings.DisabledTools[string(key.Kind)+":"+key.Name], fileToolRef(identity))
	return ToolOverrideVersion{Identity: &identity, Scope: scope, Enabled: !blocked, Present: blocked, Version: digest}, nil
}

func (s *ToolOverrideStore) setFile(ctx context.Context, identity ToolIdentity, scope, userID, agentID string, enabled bool, expected string) (ToolOverrideVersion, error) {
	if s == nil || s.files == nil {
		return ToolOverrideVersion{}, fmt.Errorf("tool override: file policy unavailable")
	}
	key, err := filePolicyTarget(identity, scope, userID, agentID)
	if err != nil {
		return ToolOverrideVersion{}, err
	}
	authority, err := fileAuthority(ctx, userID, agentID)
	if err != nil {
		return ToolOverrideVersion{}, err
	}
	access, err := s.files.Begin(authority)
	if err != nil {
		return ToolOverrideVersion{}, err
	}
	if err := access.AuthorizeKey(ctx, key, true); err != nil {
		return ToolOverrideVersion{}, err
	}
	settings, digest, err := access.ReadSettings(ctx, key.Scope, key.AgentID)
	if err != nil {
		return ToolOverrideVersion{}, err
	}
	if expected != "" && expected != ToolOverrideAbsentVersion && expected != digest {
		return ToolOverrideVersion{}, config.ErrAgentVersionConflict
	}
	if expected == ToolOverrideAbsentVersion && digest != "" {
		// An absent tool row is only creatable against an empty settings root.
		// Management lists a non-empty root digest for file resources, so a stale
		// legacy "absent" write cannot erase another settings update.
		return ToolOverrideVersion{}, config.ErrAgentVersionConflict
	}
	name := string(key.Kind) + ":" + key.Name
	values := slices.Clone(settings.DisabledTools[name])
	ref := fileToolRef(identity)
	if enabled {
		values = slices.DeleteFunc(values, func(value string) bool { return value == ref })
	} else if !slices.Contains(values, ref) {
		values = append(values, ref)
	}
	resource, err := access.SetDisabledTools(ctx, key.ID(), digest, values)
	if err != nil {
		if errors.Is(err, internalplugin.ErrConflict) {
			return ToolOverrideVersion{}, config.ErrAgentVersionConflict
		}
		return ToolOverrideVersion{}, err
	}
	return ToolOverrideVersion{Identity: &identity, Scope: scope, Enabled: enabled, Present: !enabled, Version: resource.SettingsDigest}, nil
}

func (s *ToolOverrideStore) clearFile(ctx context.Context, identity ToolIdentity, scope, userID, agentID, expected string) (ToolOverrideVersion, error) {
	current, err := s.getFile(ctx, identity, scope, userID, agentID)
	if err != nil {
		return ToolOverrideVersion{}, err
	}
	if !current.Present {
		if expected == "" {
			return current, nil
		}
		return ToolOverrideVersion{}, config.ErrAgentVersionConflict
	}
	return s.setFile(ctx, identity, scope, userID, agentID, true, expected)
}

func fileAuthority(ctx context.Context, userID, agentID string) (authz.Authority, error) {
	if authority, ok := authz.AuthorityFromContext(ctx); ok && authority.Valid() {
		return authority, nil
	}
	return authz.Authority{}, authz.ErrUnauthenticated
}
