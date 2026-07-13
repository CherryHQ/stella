package skills

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/CherryHQ/stella/internal/authz"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	"github.com/CherryHQ/stella/pkg/tools"
)

var skillsInputSchema = func() map[string]any {
	var m map[string]any
	_ = json.Unmarshal([]byte(`{
  "type": "object",
  "properties": {
    "action": {
      "type": "string",
      "enum": ["load", "search_installed", "search", "install", "list", "remove", "create", "patch", "deprecate"],
      "description": "Action to perform: 'load' reads a skill's content by name, 'search_installed' searches installed visible skills, 'search' finds skills from the remote ecosystem, 'install' adds a skill, 'list' shows installed skills, 'remove' deletes an installed skill, 'create' creates a new skill (draft), 'patch' updates an existing skill's fields, 'deprecate' marks a skill as deprecated"
    },
    "query": {
      "type": "string",
      "description": "Search query (required for search and search_installed)"
    },
    "limit": {
      "type": "integer",
      "description": "Max results to return (default 10, for search and search_installed)"
    },
    "source": {
      "type": "string",
      "description": "Skill source to install. Supports: 'clawhub:<slug>' or 'clawhub:<slug>@<version>' (from clawhub.ai), 'owner/repo@skill-name' (GitHub shorthand), 'owner/repo@skill-name#ref' (with branch/tag), GitHub/GitLab URLs, or local paths (required for install)"
    },
    "scope": {
      "type": "string",
      "enum": ["user", "agent"],
      "description": "Writable scope for install/remove/create/patch/deprecate. Defaults to 'user'. Set to 'agent' to target the current agent scope. Project skills are read-only (they live in {PROJECT_ROOT}/.agents/skills and come with the repo)."
    },
    "name": {
      "type": "string",
      "description": "Name of the skill (required for load, remove, create, patch, deprecate)"
    },
    "description": {
      "type": "string",
      "description": "Skill description (required for create, optional for patch)"
    },
    "content": {
      "type": "string",
      "description": "Skill body content in markdown (optional for create and patch)"
    },
    "status": {
      "type": "string",
      "enum": ["draft", "active", "deprecated"],
      "description": "Skill status (optional for patch)"
    },
    "path": {
      "type": "string",
      "description": "Relative path within the skill to load (e.g. references/api.md). Defaults to SKILL.md"
    }
  },
  "required": ["action"]
}`), &m)
	return m
}()

const runtimeUsageTouchTimeout = 500 * time.Millisecond

type Tool struct {
	svc         *Service
	store       pkgplugins.SkillStore
	stellaHome  string
	projectRoot string
	actionsOnly map[string]bool
	// layout is the single authority for where a DB-backed skill's files live on
	// disk, by scope; it must agree with the write side that materialized them.
	layout SkillDiskLayout
	view   SkillDirView
	// Plugin visibility is captured at runner construction so tool search and
	// prompt search instructions use the same visible system-skill set.
	registeredPluginIDs []string
	enabledPluginIDs    []string
}

type reflectSkillRuntimeUsageTracker interface {
	TouchReflectSkillRuntimeUse(ctx context.Context, skillID string, userID string, agentID string) error
}

func NewTool(store pkgplugins.SkillStore, stellaHome, projectRoot string) *Tool {
	return &Tool{
		svc:         NewService(store, stellaHome),
		store:       store,
		stellaHome:  stellaHome,
		projectRoot: projectRoot,
	}
}

// WithSkillDiskLayout sets where DB-backed skills live on disk, by scope. The
// emitted <skill_dir> for a DB skill comes from here, so it must match the dirs
// the disk-mirroring writer used. The zero value emits no dir for DB skills.
func (t *Tool) WithSkillDiskLayout(l SkillDiskLayout) *Tool {
	t.layout = l
	return t
}

// WithSkillDirView sets how host skill directories are remapped to the
// model-visible paths the agent sees inside the sandbox. The default (zero value)
// emits host paths, which is correct for host-execution and non-sandbox callers.
func (t *Tool) WithSkillDirView(v SkillDirView) *Tool {
	t.view = v
	return t
}

// WithPluginVisibility limits plugin-owned system skills to enabled plugins.
func (t *Tool) WithPluginVisibility(registered, enabled []string) *Tool {
	t.registeredPluginIDs = append([]string(nil), registered...)
	t.enabledPluginIDs = append([]string(nil), enabled...)
	return t
}

// WithActionsOnly restricts the model-facing tool to the named actions. The
// executor enforces the same allowlist so hidden actions cannot be called by
// submitting raw tool arguments.
func (t *Tool) WithActionsOnly(actions ...string) *Tool {
	t.actionsOnly = make(map[string]bool, len(actions))
	for _, action := range actions {
		t.actionsOnly[action] = true
	}
	return t
}

// SkillDirView remaps a host skill directory to the path the agent sees inside
// the sandbox, so an emitted <skill_dir> is usable in bash and never leaks a host
// path. The zero value is identity (host paths emitted) for host-execution and
// non-sandbox callers. For an isolating backend, set Isolated and the host→view
// root pairs; a skill dir under no known root is then omitted rather than leaked.
type SkillDirView struct {
	Isolated bool
	// SystemSkillsHost/View map the system skills dir specifically (not all of
	// STELLA_HOME): only that subtree is mounted, so a broad STELLA_HOME mapping
	// would wrongly swallow the sibling users/ and agents/ trees nested under it.
	SystemSkillsHost string
	SystemSkillsView string
	// AgentSkillsHost/View map the admin-managed agent-bound (system_agent) skills
	// dir, mounted at its own fixed path (/opt/stella/agent-skills) since it lives
	// in the user-independent agent definition tree, outside the two roots.
	AgentSkillsHost string
	AgentSkillsView string
	// SystemDBSkillsHost/View map the DB-installed system skills dir (a sibling of
	// the shipped built-ins under STELLA_HOME), mounted at its own fixed path
	// (/opt/stella/db-skills); the built-ins map via SystemSkillsHost/View.
	SystemDBSkillsHost string
	SystemDBSkillsView string
	// UserData and Workspace are full binds, so their whole root maps (this lets
	// project skills under <workspace>/projects/<id>/.agents/skills map too).
	UserDataHost  string
	UserDataView  string
	WorkspaceHost string
	WorkspaceView string
}

// apply remaps hostDir to its model-visible path. It returns "" to omit the dir
// when an isolating backend has no mounted root containing hostDir (emitting the
// host path would leak the host layout and would not resolve in the sandbox).
func (v SkillDirView) apply(hostDir string) string {
	if hostDir == "" {
		return ""
	}
	clean := filepath.Clean(hostDir)
	for _, m := range [][2]string{
		{v.WorkspaceHost, v.WorkspaceView},
		{v.UserDataHost, v.UserDataView},
		{v.SystemSkillsHost, v.SystemSkillsView},
		{v.AgentSkillsHost, v.AgentSkillsView},
		{v.SystemDBSkillsHost, v.SystemDBSkillsView},
	} {
		if m[0] == "" {
			continue
		}
		host := filepath.Clean(m[0])
		if clean == host {
			return m[1]
		}
		if rel, err := filepath.Rel(host, clean); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return filepath.Join(m[1], rel)
		}
	}
	if v.Isolated {
		return ""
	}
	return hostDir
}

const (
	skillScopeUser = "user"
	// The model-facing "agent" scope persists as user_agent: the user's own
	// skills bound to the current agent. The model runs on behalf of a user who
	// may not be an admin, so it must not write the admin-managed system_agent
	// scope — those are managed in Settings → Skills. See normalizeSkillScope.
	skillScopeAgent = "user_agent"
)

// errProjectScopeWriteRejected is the error returned when a write action is attempted on project scope.
const errProjectScopeMsg = "scope=project is not supported for write operations — project skills live in {PROJECT_ROOT}/.agents/skills and come with the repo; edit the files directly in git"

func normalizeSkillScope(scope string) (string, error) {
	scope = filepath.Clean(scope)
	switch scope {
	case "", ".", skillScopeUser:
		return skillScopeUser, nil
	case "agent", skillScopeAgent:
		return skillScopeAgent, nil
	case "project":
		return "", errors.New(errProjectScopeMsg)
	default:
		return "", fmt.Errorf("invalid scope %q, expected user or agent", scope)
	}
}

func (t *Tool) targetScope(ctx context.Context, rawScope string) (string, error) {
	scope, err := normalizeSkillScope(rawScope)
	if err != nil {
		return "", err
	}
	switch scope {
	case skillScopeUser:
		// User-scope skills are owned by the requesting user; without a user in
		// context there is no owner to attribute them to. Where they materialize on
		// disk is the store's concern (DiskSyncStore), not the tool's — the tool
		// carries no skill-path knowledge for writes.
		//
		// Group sessions deliberately leave the user unset (D9: runtime identity
		// stays the group, see runtime/chat.go), so user-scope writes are refused
		// here. That is intentional: the old layout-based gate let them through but
		// create() then stamped an empty owner — which fails late on the user_id
		// foreign key under normal enforcement, or (without it) leaves a dead row the
		// store never resolves and DiskSyncStore never materializes. This fails fast.
		if authz.UserIDFromContext(ctx) == "" {
			return "", fmt.Errorf("user skill scope is unavailable without a user context")
		}
		return scope, nil
	case skillScopeAgent:
		return scope, nil
	default:
		return "", fmt.Errorf("unsupported scope %q", scope)
	}
}

// viewContext builds a SkillViewContext from the request context.
func (t *Tool) viewContext(ctx context.Context) pkgplugins.SkillViewContext {
	return pkgplugins.SkillViewContext{
		UserID:  authz.UserIDFromContext(ctx),
		AgentID: authz.AgentIDFromContext(ctx),
	}
}

func pkgskillsToolDefinition() tools.Definition {
	return tools.Definition{
		Name:        "skills",
		Description: "Manage agent skills. Use 'search_installed' to discover installed visible skills by task query, then 'load' to read a selected skill by name. Use 'search' only to find remote ecosystem skills for installation. Use 'install' to add a skill (scope=user by default, or scope=agent), 'list' to see installed skills, 'remove' to delete one, 'create' to create a new skill (draft), 'patch' to update fields, 'deprecate' to mark as deprecated. Project skills come with the repo and are read-only — edit their files in git directly.",
		InputSchema: skillsInputSchema,
	}
}

func (t *Tool) Definition() tools.Definition {
	definition := pkgskillsToolDefinition()
	if t.actionsOnly == nil {
		return definition
	}
	definition.Description = "Search installed visible skills and load a selected skill's content."
	definition.InputSchema = t.restrictedInputSchema()
	return definition
}

func (t *Tool) Execute(ctx context.Context, args map[string]any) (string, error) {
	action, _ := args["action"].(string)
	if t.actionsOnly != nil && !t.actionsOnly[action] {
		return "", fmt.Errorf("skills action %q is not available in this context", action)
	}
	switch action {
	case "load":
		return t.load(ctx, args)
	case "search_installed":
		return t.searchInstalled(ctx, args)
	case "search":
		return t.search(ctx, args)
	case "install":
		return t.install(ctx, args)
	case "list":
		return t.list(ctx)
	case "remove":
		return t.remove(ctx, args)
	case "create":
		return t.create(ctx, args)
	case "patch":
		return t.patch(ctx, args)
	case "deprecate":
		return t.deprecate(ctx, args)
	default:
		return "", fmt.Errorf("unknown action %q, expected load/search_installed/search/install/list/remove/create/patch/deprecate", action)
	}
}

func (t *Tool) restrictedInputSchema() map[string]any {
	properties := map[string]any{
		"action": map[string]any{
			"type":        "string",
			"enum":        t.allowedActionValues(),
			"description": "Action to perform",
		},
	}
	baseProperties, _ := skillsInputSchema["properties"].(map[string]any)
	for property, actions := range map[string][]string{
		"query":       {"search_installed", "search"},
		"limit":       {"search_installed", "search"},
		"source":      {"install"},
		"scope":       {"install", "remove", "create", "patch", "deprecate"},
		"name":        {"load", "remove", "create", "patch", "deprecate"},
		"description": {"create", "patch"},
		"content":     {"create", "patch"},
		"status":      {"patch"},
		"path":        {"load"},
	} {
		if t.allowsAny(actions...) {
			properties[property] = baseProperties[property]
		}
	}
	return map[string]any{
		"type":       "object",
		"properties": properties,
		"required":   []any{"action"},
	}
}

func (t *Tool) allowedActionValues() []any {
	all, _ := skillsInputSchema["properties"].(map[string]any)
	action, _ := all["action"].(map[string]any)
	values, _ := action["enum"].([]any)
	allowed := make([]any, 0, len(values))
	for _, raw := range values {
		name, _ := raw.(string)
		if t.actionsOnly[name] {
			allowed = append(allowed, name)
		}
	}
	return allowed
}

func (t *Tool) allowsAny(actions ...string) bool {
	for _, action := range actions {
		if t.actionsOnly[action] {
			return true
		}
	}
	return false
}

func (t *Tool) load(ctx context.Context, args map[string]any) (string, error) {
	name, _ := args["name"].(string)
	if name == "" {
		return "", fmt.Errorf("name is required for load action")
	}

	path, _ := args["path"].(string)
	projectRoot := projectRootFromContext(ctx, t.projectRoot)
	vc := t.viewContext(ctx)

	data, skillDir, resolved, err := t.svc.LoadFile(ctx, name, path, vc, projectRoot)
	if err != nil {
		return "", err
	}

	if path == "" {
		path = pkgplugins.SkillMainFile
	}

	// For DB skills without a dir from the service, resolve the disk path the
	// writer materialized them to (the layout is the shared authority). Materialize
	// on load as a repair path for existing rows that predate disk mirroring or were
	// created through a non-syncing store; <skill_dir> must be executable, not a
	// theoretical address.
	if skillDir == "" {
		if resolved != nil {
			skillDir = t.layout.Dir(resolved.Scope, resolved.Name)
			if skillDir != "" {
				// Cross-replica writes leave no local signal, so load refreshes the mirror
				// from the DB; content comparison spares the disk writes but not the DB
				// reads. Ceiling: a revision marker would make the no-op path cheap, but
				// needs file mutations to bump skill.updated_at first (today
				// UpsertSkillFile/DeleteSkillFile touch only skill_file rows).
				if err := t.materializeDBSkill(ctx, resolved.ID, skillDir); err != nil {
					slog.WarnContext(ctx, "skills load: failed to materialize DB skill", "skill", resolved.Name, "dir", skillDir, "err", err)
					// Degrade to staleness, not to lost execution: keep serving an
					// existing mirror through a transient store error. Probed by
					// opening — the sandbox bypass guard bans the stat helpers here.
					if f, openErr := os.Open(filepath.Join(skillDir, pkgplugins.SkillMainFile)); openErr != nil {
						skillDir = ""
					} else {
						_ = f.Close()
					}
				}
			}
		}
	}
	t.touchReflectSkillRuntimeUse(ctx, resolved, vc)

	// Remap the host directory to the path the agent sees inside the sandbox; an
	// unmappable dir on an isolating backend is dropped rather than leaked.
	skillDir = t.view.apply(skillDir)

	var out strings.Builder
	if skillDir != "" {
		fmt.Fprintf(&out, "<skill_dir>%s</skill_dir>\n", skillDir)
	}
	fmt.Fprintf(&out, "<skill_content name=%q path=%q>\n%s\n</skill_content>", name, path, data)

	return out.String(), nil
}

func (t *Tool) touchReflectSkillRuntimeUse(ctx context.Context, resolved *ResolvedSkill, vc pkgplugins.SkillViewContext) {
	tracker, ok := t.store.(reflectSkillRuntimeUsageTracker)
	if !ok || vc.UserID == "" || vc.AgentID == "" {
		return
	}
	if resolved == nil || resolved.Scope != skillScopeAgent || resolved.UserID != vc.UserID || resolved.AgentID != vc.AgentID {
		return
	}
	if !IsReflectOwned(Skill{Metadata: resolved.Metadata}) {
		return
	}
	touchCtx, cancel := context.WithTimeout(ctx, runtimeUsageTouchTimeout)
	defer cancel()
	if err := tracker.TouchReflectSkillRuntimeUse(touchCtx, resolved.ID, vc.UserID, vc.AgentID); err != nil {
		slog.WarnContext(ctx, "skills load: failed to touch skill usage", "skill", resolved.Name, "err", err)
	}
}

type installedSkill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Status      string `json:"status"`
	Scope       string `json:"scope"`
	Removable   bool   `json:"removable"`
}

func (t *Tool) materializeDBSkill(ctx context.Context, skillID, skillDir string) error {
	if t.store == nil || skillID == "" || skillDir == "" {
		return nil
	}
	paths, err := t.store.ListFiles(ctx, skillID)
	if err != nil {
		return fmt.Errorf("materialize skill files: %w", err)
	}
	// A loadable skill always has SKILL.md in the store; an empty listing is an
	// inconsistency, and pruning against it would wipe the whole mirror.
	if len(paths) == 0 {
		return fmt.Errorf("materialize skill files: store listed no files for skill %s", skillID)
	}
	seen := make(map[string]struct{}, len(paths))
	for _, p := range paths {
		content, err := t.store.LoadFile(ctx, skillID, p)
		if err != nil {
			return fmt.Errorf("materialize skill file %q: %w", p, err)
		}
		diskPath, err := safeSkillDiskPath(skillDir, filepath.FromSlash(p))
		if err != nil {
			return fmt.Errorf("materialize skill file %q: %w", p, err)
		}
		if err := os.MkdirAll(filepath.Dir(diskPath), 0o755); err != nil {
			return fmt.Errorf("materialize skill file %q: %w", p, err)
		}
		data := []byte(content)
		if existing, err := readDiskFile(diskPath); err == nil && bytes.Equal(existing, data) {
			// Already current.
		} else if err := os.WriteFile(diskPath, data, 0o644); err != nil {
			return fmt.Errorf("materialize skill file %q: %w", p, err)
		}
		rel, err := filepath.Rel(skillDir, diskPath)
		if err != nil {
			return fmt.Errorf("materialize skill file %q: %w", p, err)
		}
		seen[filepath.ToSlash(rel)] = struct{}{}
	}
	if err := filepath.WalkDir(skillDir, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return walkErr
		}
		rel, err := filepath.Rel(skillDir, p)
		if err != nil {
			return err
		}
		if _, ok := seen[filepath.ToSlash(rel)]; !ok {
			_ = os.Remove(p)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("materialize skill files: prune stale files: %w", err)
	}
	return nil
}

func readDiskFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close() //nolint:errcheck
	return io.ReadAll(f)
}

func safeSkillDiskPath(base string, parts ...string) (string, error) {
	joined := filepath.Join(append([]string{base}, parts...)...)
	cleaned := filepath.Clean(joined)
	base = filepath.Clean(base)
	if cleaned != base && !strings.HasPrefix(cleaned, base+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes base %q", cleaned, base)
	}
	return cleaned, nil
}

func (t *Tool) list(ctx context.Context) (string, error) {
	projectRoot := projectRootFromContext(ctx, t.projectRoot)
	vc := t.viewContext(ctx)

	merged, err := t.svc.ListMerged(ctx, vc, projectRoot)
	if err != nil {
		return "", fmt.Errorf("list skills: %w", err)
	}

	if len(merged) == 0 {
		return "No skills installed.", nil
	}

	results := make([]installedSkill, 0, len(merged))
	for _, rs := range merged {
		results = append(results, installedSkill{
			Name:        rs.Name,
			Description: rs.Description,
			Status:      rs.Status,
			Scope:       rs.Scope,
			Removable:   IsWritable(rs.Scope),
		})
	}

	out, _ := json.MarshalIndent(results, "", "  ")
	return string(out), nil
}

func (t *Tool) create(ctx context.Context, args map[string]any) (string, error) {
	name, _ := args["name"].(string)
	description, _ := args["description"].(string)
	content, _ := args["content"].(string)

	// Validate name/description before touching the store.
	if errs := validateCreateInput(name, description); len(errs) > 0 {
		return "", fmt.Errorf("validation failed: %s", joinErrs(errs))
	}

	rawScope, err := scopeArg(args)
	if err != nil {
		return "", err
	}
	scope, err := t.targetScope(ctx, rawScope)
	if err != nil {
		return "", err
	}

	if t.store == nil {
		return "", fmt.Errorf("skills store unavailable")
	}

	if content == "" {
		content = fmt.Sprintf("# %s\n", name)
	}

	createdAt := time.Now().UTC().Format(time.RFC3339)
	mainContent := buildSkillFile(name, description, SkillStatusDraft, createdAt, content)

	sk := pkgplugins.Skill{
		Scope:       scope,
		Name:        name,
		Description: description,
		Status:      SkillStatusDraft,
		// Knowledge facts are stored in the facts table; the skills tool only
		// creates reusable procedures that can be listed and loaded as skills.
		DisableModelInvocation: false,
	}
	metaJSON := fmt.Sprintf(`{"created-at":%q}`, createdAt)
	sk.Metadata = json.RawMessage(metaJSON)

	vc := t.viewContext(ctx)
	switch scope {
	case "user":
		sk.UserID = vc.UserID
	case "user_agent":
		sk.UserID = vc.UserID
		sk.AgentID = vc.AgentID
	case "system_agent":
		sk.AgentID = vc.AgentID
	}

	files := map[string]string{pkgplugins.SkillMainFile: mainContent}
	if _, err := t.store.Create(ctx, sk, files); err != nil {
		return "", fmt.Errorf("create skill %q: %w", name, err)
	}

	return fmt.Sprintf("Skill %q created as draft (scope=%s).", name, scope), nil
}

func (t *Tool) patch(ctx context.Context, args map[string]any) (string, error) {
	name, _ := args["name"].(string)
	if name == "" {
		return "", fmt.Errorf("name is required for patch action")
	}

	s, err := t.resolveWritableSkill(ctx, name, args)
	if err != nil {
		return "", err
	}

	p := pkgplugins.SkillUpdatePatch{}
	if v, ok := args["description"].(string); ok && v != "" {
		p.Description = &v
	}
	if v, ok := args["status"].(string); ok && v != "" {
		normalized := NormalizeSkillStatus(v)
		p.Status = &normalized
	}

	if err := t.store.Update(ctx, s.ID, p); err != nil {
		return "", fmt.Errorf("patch skill %q: %w", name, err)
	}

	if content, ok := args["content"].(string); ok && content != "" {
		if err := t.store.UpsertFile(ctx, s.ID, pkgplugins.SkillMainFile, content); err != nil {
			return "", fmt.Errorf("patch skill %q content: %w", name, err)
		}
	}

	return fmt.Sprintf("Skill %q updated.", name), nil
}

func (t *Tool) deprecate(ctx context.Context, args map[string]any) (string, error) {
	name, _ := args["name"].(string)
	if name == "" {
		return "", fmt.Errorf("name is required for deprecate action")
	}

	s, err := t.resolveWritableSkill(ctx, name, args)
	if err != nil {
		return "", err
	}

	if err := t.store.Delete(ctx, s.ID); err != nil {
		return "", fmt.Errorf("deprecate skill %q: %w", name, err)
	}

	return fmt.Sprintf("Skill %q deprecated.", name), nil
}

// resolveWritableSkill finds the skill a write action (remove/patch/deprecate)
// targets. Missing scope follows the tool schema and defaults to user. Same-name
// skills across scopes are expected, so write actions always resolve one exact
// writable bucket instead of using runtime precedence.
func (t *Tool) resolveWritableSkill(ctx context.Context, name string, args map[string]any) (*pkgplugins.Skill, error) {
	if t.store == nil {
		return nil, fmt.Errorf("skills store unavailable")
	}
	rawScope, err := scopeArg(args)
	if err != nil {
		return nil, err
	}
	wantScope, err := normalizeSkillScope(rawScope)
	if err != nil {
		return nil, err
	}
	vc := t.viewContext(ctx)
	rs, err := t.svc.ResolveScoped(ctx, name, wantScope, vc, projectRootFromContext(ctx, t.projectRoot))
	if err != nil {
		return nil, fmt.Errorf("resolve skill %q: %w", name, err)
	}
	if rs == nil {
		if rawScope == "" {
			return nil, fmt.Errorf("skill %q not found in default scope=user", name)
		}
		return nil, fmt.Errorf("skill %q not found in scope=%s", name, rawScope)
	}
	sk := rs.Skill
	s := &sk

	if s.Scope == "project" {
		return nil, fmt.Errorf("skill %q is a project skill — %s", name, errProjectScopeMsg)
	}
	if s.Scope != skillScopeUser && s.Scope != "user_agent" {
		return nil, fmt.Errorf("skill %q has scope %q; the skills tool only manages your user and user_agent skills — system and system_agent skills are admin-managed in Settings → Skills", name, s.Scope)
	}
	return s, nil
}

func (t *Tool) remove(ctx context.Context, args map[string]any) (string, error) {
	name, _ := args["name"].(string)
	if name == "" {
		return "", fmt.Errorf("name is required for remove action")
	}

	if err := skillNameValidationError(name, name); err != nil {
		return "", err
	}

	s, err := t.resolveWritableSkill(ctx, name, args)
	if err != nil {
		return "", err
	}

	if err := t.store.Delete(ctx, s.ID); err != nil {
		return "", fmt.Errorf("delete skill %q: %w", name, err)
	}

	return fmt.Sprintf("Skill %q removed (scope=%s).", name, s.Scope), nil
}

func scopeArg(args map[string]any) (string, error) {
	if args == nil {
		return "", nil
	}
	v, ok := args["scope"]
	if !ok || v == nil {
		return "", nil
	}
	scope, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("scope must be a string")
	}
	return scope, nil
}

func joinErrs(errs []string) string {
	if len(errs) == 0 {
		return ""
	}
	var result strings.Builder
	result.WriteString(errs[0])
	for _, e := range errs[1:] {
		result.WriteString("; " + e)
	}
	return result.String()
}
