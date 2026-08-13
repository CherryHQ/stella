package skills

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/CherryHQ/stella/internal/authz"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
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
      "description": "Action to perform: 'load' reads a skill's content by name, 'search_installed' searches installed visible skills, 'search' finds skills from the remote ecosystem, 'install' adds a skill, 'list' shows installed skills, 'remove' deletes an installed skill, 'create' creates a new active skill, 'patch' updates an existing skill's fields, 'deprecate' marks a skill as deprecated"
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
	svc             *Service
	store           pkgplugins.SkillStore
	revisions       IdentityReader
	session         sandboxSession
	stellaHome      string
	projectRoot     string
	projectSnapshot *ProjectSnapshot
	actionsOnly     map[string]bool
	view            SkillDirView
	projectionMu    sync.Mutex
	// Plugin visibility is captured at runner construction so tool search and
	// prompt search instructions use the same visible system-skill set.
	registeredPluginIDs []string
	enabledPluginIDs    []string
	disabledSkillRefs   []string
	// readAuthz enforces Skill read authorization on every DB-backed skill this tool
	// reads (load/search_installed/list). Injected from the composition root; when
	// nil, DB-backed reads fail closed (the row is dropped or reported not-found).
	readAuthz SkillReadAuthorizer
	// writeAuthz enforces Skill write authorization on every DB-backed
	// create/patch/deprecate. Only callers that expose write actions (the reflect
	// reviewer) inject it; when a write action runs without it, the write fails closed.
	writeAuthz SkillWriteAuthorizer
}

func (t *Tool) WithProjectSnapshot(snapshot *ProjectSnapshot) *Tool {
	t.projectSnapshot = snapshot
	return t
}

// errSkillNotFound is the opaque result of a denied or missing DB skill read; the
// tool never distinguishes "forbidden" from "missing" to the model.
var errSkillNotFound = errors.New("skill not found")

// errSkillWriteUnauthorized is returned when a write action runs without an
// injected write authorizer (fail closed).
var errSkillWriteUnauthorized = errors.New("skill write is not authorized in this context")

type reflectSkillRuntimeUsageTracker interface {
	TouchReflectSkillRuntimeUse(ctx context.Context, skillID string, userID string, agentID string) error
}

type reflectSkillRuntimeDigestTracker interface {
	TouchReflectSkillRuntimeUseDigest(ctx context.Context, skillID string, userID string, agentID string, digest string) error
}

type sandboxSession interface {
	Policy() pkgsandbox.Policy
	Files() pkgsandbox.FileAccess
}

func NewTool(store pkgplugins.SkillStore, stellaHome, projectRoot string) *Tool {
	return &Tool{
		svc:         NewService(store, stellaHome),
		store:       store,
		stellaHome:  stellaHome,
		projectRoot: projectRoot,
	}
}

// WithManagedRevisions binds the identity-first Home reader and the already
// active sandbox Session used for one-turn execution projections.
func (t *Tool) WithManagedRevisions(reader IdentityReader, session sandboxSession) *Tool {
	t.revisions = reader
	t.session = session
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

// WithAgentSkillPolicy captures the runner's immutable policy snapshot. The
// next runner observes a committed mutation after local invalidation.
func (t *Tool) WithAgentSkillPolicy(disabled []string) *Tool {
	t.disabledSkillRefs = append([]string(nil), disabled...)
	return t
}

// WithReadAuthorizer injects the Skill read authorizer. Every DB-backed skill
// the tool would return or load is authorized per tool invocation; a denied row
// is dropped (list/search) or reported not-found (load).
func (t *Tool) WithReadAuthorizer(a SkillReadAuthorizer) *Tool {
	t.readAuthz = a
	return t
}

// authorizeReadable filters merged skills through the read PEP: filesystem
// project/built-in skills pass unchanged; each DB row is authorized under one
// evaluation. When no authorizer is injected, DB rows fail closed (are dropped).
func (t *Tool) authorizeReadable(ctx context.Context, merged []ResolvedSkill) ([]ResolvedSkill, error) {
	anyDB := slices.ContainsFunc(merged, isDBSkill)
	if !anyDB {
		return merged, nil
	}
	out := make([]ResolvedSkill, 0, len(merged))
	var dec SkillReadDecision
	if t.readAuthz != nil {
		d, err := t.readAuthz.BeginRead(ctx)
		switch {
		case err == nil:
			dec = d
		case errors.Is(err, authz.ErrUnauthenticated):
			// No authorizable identity (e.g. a group turn without a group id): drop
			// DB rows (fail hidden) rather than failing the whole read; FS project/
			// built-in skills below still pass.
			dec = nil
		default:
			return nil, err
		}
	}
	for _, rs := range merged {
		if !isDBSkill(rs) {
			out = append(out, rs)
			continue
		}
		if dec == nil {
			continue // fail closed: no decider, drop the DB row
		}
		allowed, err := dec.AllowRead(ctx, rs.ID, rs.Scope, rs.UserID, rs.AgentID)
		if err != nil {
			return nil, err
		}
		if allowed {
			out = append(out, rs)
		}
	}
	return out, nil
}

// authorizeLoadable authorizes a single resolved skill for load. A denied or
// unauthorized DB row is reported not-found; filesystem skills pass.
func (t *Tool) authorizeLoadable(ctx context.Context, rs *ResolvedSkill) error {
	if rs == nil || !isDBSkill(*rs) {
		return nil
	}
	if t.readAuthz == nil {
		return errSkillNotFound
	}
	dec, err := t.readAuthz.BeginRead(ctx)
	if err != nil {
		if errors.Is(err, authz.ErrUnauthenticated) {
			// No authorizable identity: hide the DB skill (fail hidden), don't error.
			return errSkillNotFound
		}
		return err
	}
	allowed, err := dec.AllowRead(ctx, rs.ID, rs.Scope, rs.UserID, rs.AgentID)
	if err != nil {
		return err
	}
	if !allowed {
		return errSkillNotFound
	}
	return nil
}

// WithWriteAuthorizer injects the Skill write authorizer. Every DB-backed
// create/patch/deprecate is authorized before the store mutation; a denial fails
// the write.
func (t *Tool) WithWriteAuthorizer(a SkillWriteAuthorizer) *Tool {
	t.writeAuthz = a
	return t
}

// authorizeCreate authorizes minting a new DB skill in scope for agentID.
func (t *Tool) authorizeCreate(ctx context.Context, scope, agentID string) error {
	if t.writeAuthz == nil {
		return errSkillWriteUnauthorized
	}
	dec, err := t.writeAuthz.BeginWrite(ctx)
	if err != nil {
		return err
	}
	return dec.AllowCreate(ctx, scope, agentID)
}

// authorizeWrite authorizes mutating an existing DB skill by id (patch/deprecate).
func (t *Tool) authorizeWrite(ctx context.Context, id string) error {
	if t.writeAuthz == nil {
		return errSkillWriteUnauthorized
	}
	dec, err := t.writeAuthz.BeginWrite(ctx)
	if err != nil {
		return err
	}
	return dec.AllowWrite(ctx, id)
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
	// BuiltinSkillsHost/View map the exact immutable release bundle projection.
	BuiltinSkillsHost string
	BuiltinSkillsView string
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
		{v.BuiltinSkillsHost, v.BuiltinSkillsView},
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
		// context there is no owner to attribute them to.
		//
		// Group sessions deliberately leave the user unset (D9: runtime identity
		// stays the group, see runtime/chat.go), so user-scope writes are refused
		// here. That is intentional: the old layout-based gate let them through but
		// create() then stamped an empty owner — which fails late on the user_id
		// foreign key under normal enforcement, or (without it) leaves a dead row the
		// store never resolves. This fails fast.
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
		UserID:            authz.UserIDFromContext(ctx),
		AgentID:           authz.AgentIDFromContext(ctx),
		DisabledSkillRefs: t.disabledSkillRefs,
	}
}

func pkgskillsToolDefinition() tools.Definition {
	return tools.Definition{
		Name:        "skills",
		Description: "Manage agent skills. Use 'search_installed' to discover installed visible skills by task query, then 'load' to read a selected skill by name. Use 'search' only to find remote ecosystem skills for installation. Use 'install' to add a skill (scope=user by default, or scope=agent), 'list' to see installed skills, 'remove' to delete one, 'create' to create a new active skill, 'patch' to update fields, and 'deprecate' to mark as deprecated. Project skills come with the repo and are read-only — edit their files in git directly.",
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
	ctx = WithProjectSnapshot(ctx, t.projectSnapshot)
	name, _ := args["name"].(string)
	if name == "" {
		return "", fmt.Errorf("name is required for load action")
	}

	path, _ := args["path"].(string)
	projectRoot := projectRootFromContext(ctx, t.projectRoot)
	vc := t.viewContext(ctx)

	if t.revisions != nil {
		return t.loadManagedOrImmutable(ctx, name, path, vc)
	}
	data, skillDir, resolved, err := t.svc.LoadFile(ctx, name, path, vc, projectRoot)
	if err != nil {
		return "", err
	}
	// A DB-backed skill is served only after a Skill read authorization; a
	// filesystem project/built-in skill is not gated. A denial is opaque.
	if err := t.authorizeLoadable(ctx, resolved); err != nil {
		return "", err
	}
	// Reflect-owned skills must claim runtime use before any executable directory
	// is materialized or exposed. A concurrent curator delete makes the claim
	// affect zero rows, causing this load to fail closed instead of serving a
	// deleted skill.
	if err := t.touchReflectSkillRuntimeUse(ctx, resolved, vc); err != nil {
		return "", err
	}

	if path == "" {
		path = pkgplugins.SkillMainFile
	}

	// Remap the host directory to the path the agent sees inside the sandbox; an
	// unmappable dir on an isolating backend is dropped rather than leaked.
	if resolved == nil || resolved.Scope != "project" {
		skillDir = t.view.apply(skillDir)
	}

	var out strings.Builder
	if skillDir != "" {
		fmt.Fprintf(&out, "<skill_dir>%s</skill_dir>\n", skillDir)
	}
	fmt.Fprintf(&out, "<skill_content name=%q path=%q>\n%s\n</skill_content>", name, path, data)

	return out.String(), nil
}

func internalSkillToPlugin(skill Skill) pkgplugins.Skill {
	return pkgplugins.Skill{
		ID: skill.ID, Scope: skill.Scope, UserID: skill.UserID, AgentID: skill.AgentID,
		Name: skill.Name, Description: skill.Description, Status: skill.Status,
		DisableModelInvocation: skill.DisableModelInvocation, Metadata: skill.Metadata,
		CreatedAt: skill.CreatedAt, UpdatedAt: skill.UpdatedAt, Version: skill.Version, ContentDigest: skill.ContentDigest,
	}
}

func resolvedIdentity(rs ResolvedSkill) Skill {
	return Skill{ID: rs.ID, Scope: rs.Scope, UserID: rs.UserID, AgentID: rs.AgentID, Name: rs.Name}
}

func (t *Tool) identityMerged(ctx context.Context, vc pkgplugins.SkillViewContext) ([]ResolvedSkill, error) {
	rows, err := t.revisions.ListIdentityVisible(ctx, ViewContext{UserID: vc.UserID, AgentID: vc.AgentID})
	if err != nil {
		return nil, err
	}
	db := make([]pkgplugins.Skill, len(rows))
	for i := range rows {
		db[i] = internalSkillToPlugin(rows[i])
	}
	merged := t.svc.ListMergedWithDBSnapshot(db, t.projectSnapshot)
	return filterDisabled(merged, vc.DisabledSkillRefs), nil
}

func (t *Tool) hydrateAuthorized(ctx context.Context, merged []ResolvedSkill) ([]ResolvedSkill, error) {
	authorized, err := t.authorizeReadable(ctx, merged)
	if err != nil {
		return nil, err
	}
	out := make([]ResolvedSkill, 0, len(authorized))
	for _, rs := range authorized {
		if !isDBSkill(rs) {
			out = append(out, rs)
			continue
		}
		revision, err := t.revisions.LoadCurrentRevision(ctx, resolvedIdentity(rs))
		if errors.Is(err, errCurrentSkillSelectorMissing) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !sameSkillIdentity(resolvedIdentity(rs), revision.Skill) {
			return nil, ErrInvalidSkillRevision
		}
		if !invocationVisible(revision.Skill) {
			continue
		}
		rs.Skill = internalSkillToPlugin(revision.Skill)
		out = append(out, rs)
	}
	return out, nil
}

func (t *Tool) loadManagedOrImmutable(ctx context.Context, name, filename string, vc pkgplugins.SkillViewContext) (string, error) {
	if filename == "" {
		filename = MainFile
	}
	if err := validateSkillPath(filename); err != nil {
		return "", err
	}
	merged, err := t.identityMerged(ctx, vc)
	if err != nil {
		return "", fmt.Errorf("resolve skill %q: %w", name, err)
	}
	var resolved *ResolvedSkill
	for i := range merged {
		if merged[i].Name == name {
			resolved = &merged[i]
			break
		}
	}
	if resolved == nil {
		return "", fmt.Errorf("skill %q not found", name)
	}
	if err := t.authorizeLoadable(ctx, resolved); err != nil {
		return "", err
	}

	var data, skillDir string
	if !isDBSkill(*resolved) {
		data, err = resolved.LoadImmutableFile(filename)
		skillDir = resolved.Dir
		if err != nil {
			return "", fmt.Errorf("load %s skill %q file %q: %w", resolved.Scope, name, filename, err)
		}
		if resolved.Scope != "project" {
			skillDir = t.view.apply(skillDir)
		}
	} else {
		revision, readErr := t.revisions.LoadCurrentRevision(ctx, resolvedIdentity(*resolved))
		if readErr != nil {
			return "", fmt.Errorf("load skill %q: %w", name, readErr)
		}
		if !sameSkillIdentity(resolvedIdentity(*resolved), revision.Skill) || !validSkillDigest(revision.Skill.ContentDigest) {
			return "", ErrInvalidSkillRevision
		}
		if !invocationVisible(revision.Skill) {
			return "", errSkillNotFound
		}
		dataBytes, ok := revision.Files[filename]
		if !ok {
			return "", fmt.Errorf("load skill %q file %q: %w", name, filename, fs.ErrNotExist)
		}
		resolved.Skill = internalSkillToPlugin(revision.Skill)
		if err := t.touchReflectSkillRuntimeUse(ctx, resolved, vc); err != nil {
			return "", err
		}
		skillDir, err = t.projectRevision(revision)
		if err != nil {
			return "", fmt.Errorf("project skill %q: %w", name, err)
		}
		data = string(dataBytes)
	}

	var out strings.Builder
	if skillDir != "" {
		fmt.Fprintf(&out, "<skill_dir>%s</skill_dir>\n", skillDir)
	}
	fmt.Fprintf(&out, "<skill_content name=%q path=%q>\n%s\n</skill_content>", name, filename, data)
	return out.String(), nil
}

func (t *Tool) projectRevision(revision ManagedRevision) (string, error) {
	// One Tool belongs to one active Session. Serialize publication so concurrent
	// loads cannot replace a digest path while another publication is deciding
	// its exact Session view.
	t.projectionMu.Lock()
	defer t.projectionMu.Unlock()

	if t.session == nil {
		return "", errors.New("active sandbox Session is required")
	}
	if !validInventoryComponent(revision.Skill.Scope) || !validInventoryComponent(revision.Skill.ID) || !validSkillDigest(revision.Skill.ContentDigest) {
		return "", ErrInvalidSkillRevision
	}
	files := make([]revisionFile, 0, len(revision.Files))
	for name, content := range revision.Files {
		mode := revision.Modes[name].Perm() & 0o555
		if mode == 0 {
			// Narrow test/compatibility readers may omit Modes. The disposable
			// projection remains readable but not writable in that case.
			mode = 0o444
		}
		files = append(files, revisionFile{Path: name, Mode: mode, Content: content})
	}
	files, err := validateRevisionFiles(files)
	if err != nil {
		return "", err
	}
	policyTemp := t.session.Policy().Env[pkgsandbox.EnvTempDir]
	if policyTemp == "" {
		return "", errors.New("sandbox Session has no TMPDIR")
	}
	visible := filepath.Join(policyTemp, "stella-skills", revision.Skill.Scope, revision.Skill.ID, revision.Skill.ContentDigest)
	projected := make([]pkgsandbox.ProjectedFile, 0, len(files))
	for _, file := range files {
		projected = append(projected, pkgsandbox.ProjectedFile{Path: file.Path, Content: file.Content, Mode: file.Mode})
	}
	// This Session-private convenience copy is atomically published and verified
	// on every load, but it is not an isolation boundary from same-UID commands.
	// A concurrent command can race that verification or modify the copy later;
	// any mismatch the next load observes fails closed instead of replacing it.
	if err := t.session.Files().ProjectFiles(visible, projected); err != nil {
		if errors.Is(err, pkgsandbox.ErrProjectionConflict) {
			return "", errors.Join(ErrInvalidSkillRevision, err)
		}
		return "", err
	}
	return visible, nil
}

func (t *Tool) touchReflectSkillRuntimeUse(ctx context.Context, resolved *ResolvedSkill, vc pkgplugins.SkillViewContext) error {
	if vc.UserID == "" || vc.AgentID == "" {
		return nil
	}
	if resolved == nil || resolved.Scope != skillScopeAgent || resolved.UserID != vc.UserID || resolved.AgentID != vc.AgentID {
		return nil
	}
	if !IsReflectOwned(Skill{Metadata: resolved.Metadata}) {
		return nil
	}
	touchCtx, cancel := context.WithTimeout(ctx, runtimeUsageTouchTimeout)
	defer cancel()
	var err error
	if tracker, ok := t.revisions.(reflectSkillRuntimeDigestTracker); ok {
		err = tracker.TouchReflectSkillRuntimeUseDigest(touchCtx, resolved.ID, vc.UserID, vc.AgentID, resolved.ContentDigest)
	} else if tracker, ok := t.store.(reflectSkillRuntimeUsageTracker); ok {
		err = tracker.TouchReflectSkillRuntimeUse(touchCtx, resolved.ID, vc.UserID, vc.AgentID)
	}
	if err != nil {
		return fmt.Errorf("claim runtime use for skill %q: %w", resolved.Name, err)
	}
	return nil
}

type installedSkill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Status      string `json:"status"`
	Scope       string `json:"scope"`
	Removable   bool   `json:"removable"`
}

func (t *Tool) list(ctx context.Context) (string, error) {
	ctx = WithProjectSnapshot(ctx, t.projectSnapshot)
	projectRoot := projectRootFromContext(ctx, t.projectRoot)
	vc := t.viewContext(ctx)

	merged, err := t.svc.ListMerged(ctx, vc, projectRoot)
	if t.revisions != nil {
		merged, err = t.identityMerged(ctx, vc)
	}
	if err != nil {
		return "", fmt.Errorf("list skills: %w", err)
	}
	if t.revisions != nil {
		merged, err = t.hydrateAuthorized(ctx, merged)
	} else {
		merged, err = t.authorizeReadable(ctx, merged)
	}
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
			Removable:   rs.builtin == nil && (rs.Scope == skillScopeUser || rs.Scope == "user_agent"),
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
	mainContent := buildSkillFile(name, description, createdAt, content)

	sk := pkgplugins.Skill{
		Scope:       scope,
		Name:        name,
		Description: description,
		Status:      SkillStatusActive,
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

	// Authorize the create against Skill write before touching the store.
	if err := t.authorizeCreate(ctx, scope, sk.AgentID); err != nil {
		return "", err
	}

	files := map[string]string{pkgplugins.SkillMainFile: mainContent}
	if _, err := t.store.Create(ctx, sk, files); err != nil {
		return "", fmt.Errorf("create skill %q: %w", name, err)
	}

	return fmt.Sprintf("Skill %q created (scope=%s).", name, scope), nil
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
	if err := t.authorizeWrite(ctx, s.ID); err != nil {
		return "", err
	}

	p := pkgplugins.SkillUpdatePatch{}
	if v, ok := args["description"].(string); ok && v != "" {
		p.Description = &v
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
	if err := t.authorizeWrite(ctx, s.ID); err != nil {
		return "", err
	}

	status := SkillStatusDeprecated
	if err := t.store.Update(ctx, s.ID, pkgplugins.SkillUpdatePatch{Status: &status}); err != nil {
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
		return nil, fmt.Errorf("skill %q has scope %q; the skills tool only manages your user and user_agent skills — system and system_agent skills are admin-managed in Admin Console → Deployment resources → Global Skills", name, s.Scope)
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

	// Removing a DB skill is a durable write; authorize it before the store mutation.
	// install/remove are unreachable through the current model-facing tool
	// (actionsOnly), but the write authorizer is enforced internally regardless so a
	// nil authorizer fails closed rather than deleting unguarded.
	if err := t.authorizeWrite(ctx, s.ID); err != nil {
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
