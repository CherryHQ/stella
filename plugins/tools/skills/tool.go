package skills

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/vaayne/anna/pkg/memory"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
	"github.com/vaayne/anna/pkg/tools"
)

var skillsInputSchema = func() map[string]any {
	var m map[string]any
	_ = json.Unmarshal([]byte(`{
  "type": "object",
  "properties": {
    "action": {
      "type": "string",
      "enum": ["load", "search", "install", "list", "remove", "create", "patch", "deprecate"],
      "description": "Action to perform: 'load' reads a skill's content by name, 'search' finds skills from the ecosystem, 'install' adds a skill, 'list' shows installed skills, 'remove' deletes an installed skill, 'create' creates a new skill (draft), 'patch' updates an existing skill's fields, 'deprecate' marks a skill as deprecated"
    },
    "query": {
      "type": "string",
      "description": "Search query (required for search)"
    },
    "limit": {
      "type": "integer",
      "description": "Max results to return (default 10, for search)"
    },
    "source": {
      "type": "string",
      "description": "Skill source to install. Supports: 'owner/repo@skill-name' (GitHub shorthand), 'owner/repo@skill-name#ref' (with branch/tag), GitHub/GitLab URLs, or local paths (required for install)"
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
      "description": "File path within the skill to load (optional for load, defaults to SKILL.md)"
    }
  },
  "required": ["action"]
}`), &m)
	return m
}()

type Tool struct {
	store         pkgplugins.SkillStore
	annaHome      string
	agentRoot     string
	projectRoot   string
	userSkillsDir string
}

func NewTool(store pkgplugins.SkillStore, annaHome, agentRoot, projectRoot, userSkillsDir string) *Tool {
	return &Tool{
		store:         store,
		annaHome:      annaHome,
		agentRoot:     agentRoot,
		projectRoot:   projectRoot,
		userSkillsDir: userSkillsDir,
	}
}

const (
	skillScopeUser  = "user"
	skillScopeAgent = "agent"
)

// errProjectScopeWriteRejected is the error returned when a write action is attempted on project scope.
const errProjectScopeMsg = "scope=project is not supported for write operations — project skills live in {PROJECT_ROOT}/.agents/skills and come with the repo; edit the files directly in git"

func normalizeSkillScope(scope string) (string, error) {
	scope = filepath.Clean(scope)
	switch scope {
	case "", ".", skillScopeUser:
		return skillScopeUser, nil
	case skillScopeAgent:
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
		if t.userSkillsDir == "" {
			return "", fmt.Errorf("user skill scope is unavailable")
		}
		return scope, nil
	case skillScopeAgent:
		return scope, nil
	default:
		return "", fmt.Errorf("unsupported scope %q", scope)
	}
}

// targetSkillsDir returns the scope and skill directory for a writable scope.
// Kept for backward compatibility; most callers only need the scope now.
func (t *Tool) targetSkillsDir(ctx context.Context, rawScope string) (string, string, error) {
	scope, err := t.targetScope(ctx, rawScope)
	if err != nil {
		return "", "", err
	}
	switch scope {
	case skillScopeUser:
		return scope, t.userSkillsDir, nil
	case skillScopeAgent:
		return scope, filepath.Join(t.agentRoot, ".agents", "skills"), nil
	default:
		return "", "", fmt.Errorf("unsupported scope %q", scope)
	}
}

// viewContext builds a SkillViewContext from the request context.
func (t *Tool) viewContext(ctx context.Context) pkgplugins.SkillViewContext {
	return pkgplugins.SkillViewContext{
		UserID:  memory.UserIDFromContext(ctx),
		AgentID: memory.AgentIDFromContext(ctx),
	}
}

func pkgskillsToolDefinition() tools.Definition {
	return tools.Definition{
		Name:        "skills",
		Description: "Manage agent skills. Use 'load' to read a skill by name (includes project skills from {PROJECT_ROOT}/.agents/skills), 'search' to find skills from the ecosystem, 'install' to add a skill (scope=user by default, or scope=agent), 'list' to see installed skills (includes project skills from the repo), 'remove' to delete one, 'create' to create a new skill (draft), 'patch' to update fields, 'deprecate' to mark as deprecated. Project skills come with the repo and are read-only — edit their files in git directly.",
		InputSchema: skillsInputSchema,
	}
}

func (t *Tool) Definition() tools.Definition {
	return pkgskillsToolDefinition()
}

func (t *Tool) Execute(ctx context.Context, args map[string]any) (string, error) {
	action, _ := args["action"].(string)
	switch action {
	case "load":
		return t.load(ctx, args)
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
		return "", fmt.Errorf("unknown action %q, expected load/search/install/list/remove/create/patch/deprecate", action)
	}
}

func (t *Tool) load(ctx context.Context, args map[string]any) (string, error) {
	name, _ := args["name"].(string)
	if name == "" {
		return "", fmt.Errorf("name is required for load action")
	}

	path, _ := args["path"].(string)
	if path == "" {
		path = pkgplugins.SkillMainFile
	}

	// Check project skills (filesystem) first.
	projectRoot := projectRootFromContext(ctx, t.projectRoot)
	if projectRoot != "" {
		_, dirs, err := ListProjectSkills(projectRoot)
		if err == nil {
			if skillDir, ok := dirs[name]; ok {
				data, err := loadProjectSkillFile(skillDir, path)
				if err != nil {
					return "", fmt.Errorf("load project skill %q file %q: %w", name, path, err)
				}
				return fmt.Sprintf("<skill_content name=%q path=%q>\n%s\n</skill_content>", name, path, data), nil
			}
		}
	}

	if t.store == nil {
		return "", fmt.Errorf("skills store unavailable")
	}

	vc := t.viewContext(ctx)
	s, err := t.store.Resolve(ctx, name, vc)
	if err != nil {
		return "", fmt.Errorf("resolve skill %q: %w", name, err)
	}
	if s == nil {
		return "", fmt.Errorf("skill %q not found", name)
	}

	data, err := t.store.LoadFile(ctx, s.ID, path)
	if err != nil {
		return "", fmt.Errorf("load skill %q file %q: %w", name, path, err)
	}

	return fmt.Sprintf("<skill_content name=%q path=%q>\n%s\n</skill_content>", s.Name, path, data), nil
}

type installedSkill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Status      string `json:"status"`
	Scope       string `json:"scope"`
	Removable   bool   `json:"removable"`
}

func (t *Tool) list(ctx context.Context) (string, error) {
	if t.store == nil {
		return "", fmt.Errorf("skills store unavailable")
	}
	vc := t.viewContext(ctx)
	dbSkills, err := t.store.List(ctx, vc)
	if err != nil {
		return "", fmt.Errorf("list skills: %w", err)
	}

	// Collect project skills from filesystem (project > user > agent > system precedence in presentation).
	projectRoot := projectRootFromContext(ctx, t.projectRoot)
	projSkills, _, _ := ListProjectSkills(projectRoot)

	// Deduplicate: project skills shadow same-named DB skills.
	projNames := make(map[string]bool, len(projSkills))
	for _, s := range projSkills {
		projNames[s.Name] = true
	}

	results := make([]installedSkill, 0, len(projSkills)+len(dbSkills))

	// Project skills first (highest precedence when presenting to agent).
	for _, s := range projSkills {
		results = append(results, installedSkill{
			Name:        s.Name,
			Description: s.Description,
			Status:      s.Status,
			Scope:       "project",
			Removable:   false,
		})
	}

	// DB skills, skipping names already covered by project skills.
	for _, s := range dbSkills {
		if projNames[s.Name] {
			continue
		}
		results = append(results, installedSkill{
			Name:        s.Name,
			Description: s.Description,
			Status:      s.Status,
			Scope:       s.Scope,
			Removable:   s.Scope == "user" || s.Scope == "agent",
		})
	}

	if len(results) == 0 {
		return "No skills installed.", nil
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
	}
	// Set metadata with created-at for expiry tracking.
	metaJSON := fmt.Sprintf(`{"created-at":%q}`, createdAt)
	sk.Metadata = json.RawMessage(metaJSON)

	vc := t.viewContext(ctx)
	switch scope {
	case "user":
		sk.UserID = vc.UserID
	case "agent":
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

	if t.store == nil {
		return "", fmt.Errorf("skills store unavailable")
	}

	vc := t.viewContext(ctx)
	s, err := t.store.Resolve(ctx, name, vc)
	if err != nil {
		return "", fmt.Errorf("resolve skill %q: %w", name, err)
	}
	if s == nil {
		return "", fmt.Errorf("skill %q not found", name)
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

	if t.store == nil {
		return "", fmt.Errorf("skills store unavailable")
	}

	vc := t.viewContext(ctx)
	s, err := t.store.Resolve(ctx, name, vc)
	if err != nil {
		return "", fmt.Errorf("resolve skill %q: %w", name, err)
	}
	if s == nil {
		return "", fmt.Errorf("skill %q not found", name)
	}

	status := SkillStatusDeprecated
	p := pkgplugins.SkillUpdatePatch{Status: &status}
	if err := t.store.Update(ctx, s.ID, p); err != nil {
		return "", fmt.Errorf("deprecate skill %q: %w", name, err)
	}

	return fmt.Sprintf("Skill %q deprecated.", name), nil
}

func (t *Tool) remove(ctx context.Context, args map[string]any) (string, error) {
	name, _ := args["name"].(string)
	if name == "" {
		return "", fmt.Errorf("name is required for remove action")
	}

	if err := skillNameValidationError(name, name); err != nil {
		return "", err
	}

	if t.store == nil {
		return "", fmt.Errorf("skills store unavailable")
	}

	rawScope, err := scopeArg(args)
	if err != nil {
		return "", err
	}

	var wantScope string
	if rawScope != "" {
		wantScope, err = normalizeSkillScope(rawScope)
		if err != nil {
			return "", err
		}
	}

	vc := t.viewContext(ctx)

	var s *pkgplugins.Skill
	if wantScope != "" {
		list, err := t.store.List(ctx, vc)
		if err != nil {
			return "", fmt.Errorf("list skills: %w", err)
		}
		for i := range list {
			if list[i].Name == name && list[i].Scope == wantScope {
				s = &list[i]
				break
			}
		}
		if s == nil {
			return "", fmt.Errorf("skill %q not found in scope=%s", name, wantScope)
		}
	} else {
		s, err = t.store.Resolve(ctx, name, vc)
		if err != nil {
			return "", fmt.Errorf("resolve skill %q: %w", name, err)
		}
		if s == nil {
			return "", fmt.Errorf("skill %q not found", name)
		}
	}

	if s.Scope == "project" {
		return "", fmt.Errorf("skill %q is a project skill — %s", name, errProjectScopeMsg)
	}
	if s.Scope != "user" && s.Scope != "agent" {
		return "", fmt.Errorf("skill %q has scope %q; only user/agent-scoped skills can be removed", name, s.Scope)
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
