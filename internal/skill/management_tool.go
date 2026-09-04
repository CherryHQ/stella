package skill

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/CherryHQ/stella/internal/authz"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
	"github.com/CherryHQ/stella/pkg/tools"
)

const managementToolListSibling = "settings_skill_list"

var managementToolDescriptions = map[string]string{
	"list":   "List managed Skills in one authorized scope. Results include the current content version required for a mutation.",
	"get":    "Read one managed Skill's safe metadata, file names, and current version. File contents are never returned.",
	"create": "Create one managed Skill from a complete Agent Skills directory in the sandbox. The result includes its server-selected ID and version.",
	"update": "Replace a managed Skill's complete directory using the version from settings_skill_get. Re-read after a conflict.",
	"delete": "Delete one managed Skill using the version from settings_skill_get. This removes its current selector after the identity is retired.",
}

// ManagementTool is one exact, Stella-only managed-Skill action.
type ManagementTool struct {
	spec       SettingsSkillActionTool
	management *Management
	runtime    pkgsandbox.Session
}

func NewRuntimeManagementTool(management *Management, runtime pkgsandbox.Session, spec SettingsSkillActionTool) *ManagementTool {
	return &ManagementTool{spec: spec, management: management, runtime: runtime}
}

func (t *ManagementTool) Definition() tools.Definition {
	return t.spec.Definition(managementToolDescriptions[t.spec.Action])
}

func (t *ManagementTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	if t == nil || t.management == nil {
		return "", ErrManagedSkillsUnavailable
	}
	authority, err := authz.DirectAuthority(ctx, authz.UserIDFromContext(ctx))
	if err != nil {
		return "", authz.MapToolError(t.spec.Name, managementToolListSibling, err)
	}
	out, err := SettingsSkillDispatch(ctx, skillManagementHandler{management: t.management, authority: authority, runtime: t.runtime}, t.spec.Action, args)
	if err != nil {
		return "", authz.MapToolError(t.spec.Name, managementToolListSibling, err)
	}
	return tools.MarshalResult(out)
}

type skillManagementHandler struct {
	management *Management
	authority  authz.Authority
	runtime    pkgsandbox.Session
}

const maxManagementSkillListResults = 50

type managementSkillView struct {
	ID                     string   `json:"id"`
	Scope                  string   `json:"scope"`
	AgentID                string   `json:"agent_id,omitempty"`
	Name                   string   `json:"name"`
	Description            string   `json:"description"`
	DisableModelInvocation bool     `json:"disable_model_invocation"`
	Files                  []string `json:"files"`
	Version                string   `json:"version"`
	CreatedAt              string   `json:"created_at"`
	UpdatedAt              string   `json:"updated_at"`
}

func managementSkillViewOf(revision ManagedRevision) managementSkillView {
	files := make([]string, 0, len(revision.Files))
	for path := range revision.Files {
		files = append(files, path)
	}
	sort.Strings(files)
	skill := revision.Skill
	return managementSkillView{
		ID: skill.ID, Scope: skill.Scope, AgentID: skill.AgentID, Name: skill.Name,
		Description: skill.Description, DisableModelInvocation: skill.DisableModelInvocation,
		Files: files, Version: skill.ContentDigest,
		CreatedAt: skill.CreatedAt.UTC().Format(time.RFC3339), UpdatedAt: skill.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

func managementSkillViewFromIdentity(skill Skill) managementSkillView {
	return managementSkillView{
		ID: skill.ID, Scope: skill.Scope, AgentID: skill.AgentID, Name: skill.Name,
		Description: skill.Description, DisableModelInvocation: skill.DisableModelInvocation,
		Files: []string{}, Version: skill.ContentDigest,
		CreatedAt: skill.CreatedAt.UTC().Format(time.RFC3339), UpdatedAt: skill.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

func managementSkillViewFromSnapshot(snapshot SkillSnapshot) managementSkillView {
	skill := snapshot.Skill
	files := append([]string(nil), snapshot.Files...)
	sort.Strings(files)
	return managementSkillView{
		ID: skill.ID, Scope: skill.Scope, AgentID: skill.AgentID, Name: skill.Name,
		Description: skill.Description, DisableModelInvocation: skill.DisableModelInvocation,
		Files: files, Version: skill.ContentDigest,
		CreatedAt: skill.CreatedAt.UTC().Format(time.RFC3339), UpdatedAt: skill.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

func (h skillManagementHandler) List(ctx context.Context, in SettingsSkillListInput) (any, error) {
	scope := in.Scope
	if scope == "" {
		scope = "user"
	}
	limit := in.Limit
	if limit == 0 {
		limit = maxManagementSkillListResults
	}
	if limit < 1 || limit > maxManagementSkillListResults {
		return nil, fmt.Errorf("limit must be between 1 and %d", maxManagementSkillListResults)
	}
	identities, err := h.management.List(ctx, h.authority, scope, in.TargetAgentId)
	if err != nil {
		return nil, err
	}
	truncated := len(identities) > limit
	if truncated {
		identities = identities[:limit]
	}
	out := make([]managementSkillView, 0, len(identities))
	for _, identity := range identities {
		out = append(out, managementSkillViewFromIdentity(identity))
	}
	return map[string]any{"skills": out, "truncated": truncated}, nil
}

func (h skillManagementHandler) Get(ctx context.Context, in SettingsSkillGetInput) (any, error) {
	revision, err := h.management.Get(ctx, h.authority, in.Id)
	if err != nil {
		return nil, err
	}
	return managementSkillViewOf(revision), nil
}

func (h skillManagementHandler) Create(ctx context.Context, in SettingsSkillCreateInput) (any, error) {
	pkg, err := h.readSkillPackage(ctx, in.ContentPath)
	if err != nil {
		return nil, err
	}
	snapshot, err := h.management.Create(ctx, h.authority, ManagedCreate{
		Scope: in.Scope, TargetAgentID: in.TargetAgentId, Name: pkg.frontmatter.Name, Description: pkg.frontmatter.Description,
		DisableModelInvocation: pkg.frontmatter.DisableModelInvocation, Files: pkg.files,
	})
	if err != nil {
		return nil, err
	}
	return managementSkillViewFromSnapshot(snapshot), nil
}

func (h skillManagementHandler) Update(ctx context.Context, in SettingsSkillUpdateInput) (any, error) {
	pkg, err := h.readSkillPackage(ctx, in.ContentPath)
	if err != nil {
		return nil, err
	}
	patch := UpdatePatch{Description: &pkg.frontmatter.Description, DisableModelInvocation: &pkg.frontmatter.DisableModelInvocation}
	snapshot, err := h.management.Update(ctx, h.authority, ManagedUpdate{
		ID: in.Id, ExpectedVersion: in.ExpectedVersion, Name: pkg.frontmatter.Name, Patch: patch, Version: in.Version,
		Files: pkg.files, ReplaceFiles: true, ConvertToManual: boolValue(in.ConvertToManual),
	})
	if err != nil {
		return nil, err
	}
	return managementSkillViewFromSnapshot(snapshot), nil
}

func (h skillManagementHandler) Delete(ctx context.Context, in SettingsSkillDeleteInput) (any, error) {
	if err := h.management.Delete(ctx, h.authority, in.Id, in.ExpectedVersion); err != nil {
		return nil, err
	}
	return map[string]string{"id": in.Id, "status": "deleted"}, nil
}

// Load and Search cannot be reached through this management adapter. The two
// runtime tools have a separate sandbox-read implementation and capability.
func (skillManagementHandler) Load(context.Context, SkillLoadInput) (any, error) {
	return nil, fmt.Errorf("skill_load is not a managed Skill action")
}

func (skillManagementHandler) Search(context.Context, SkillSearchInput) (any, error) {
	return nil, fmt.Errorf("skill_installed_search is not a managed Skill action")
}

type managedSkillPackage struct {
	frontmatter skillFrontmatter
	files       map[string]string
}

func (h skillManagementHandler) readSkillPackage(ctx context.Context, directoryPath string) (managedSkillPackage, error) {
	if h.runtime == nil {
		return managedSkillPackage{}, fmt.Errorf("sandbox file access is unavailable")
	}
	view, err := pkgsandbox.SelectFileView(ctx, h.runtime)
	if err != nil {
		return managedSkillPackage{}, err
	}
	resolved := directoryPath
	if strings.HasPrefix(resolved, "$") {
		resolved, err = pkgsandbox.ExpandPathVariables(resolved, view.Policy.Env)
		if err != nil {
			return managedSkillPackage{}, err
		}
	}
	resolved, err = tools.ResolvePath(view.WorkingDir, resolved)
	if err != nil {
		return managedSkillPackage{}, err
	}
	info, err := view.Files.Stat(resolved)
	if err != nil {
		return managedSkillPackage{}, err
	}
	if !info.IsDir {
		return managedSkillPackage{}, fmt.Errorf("content_path must be an Agent Skills directory containing %s", MainFile)
	}
	files, err := readManagedSkillDirectory(view.Files, resolved)
	if err != nil {
		return managedSkillPackage{}, err
	}
	main, ok := files[MainFile]
	if !ok || main == "" {
		return managedSkillPackage{}, fmt.Errorf("content_path must contain a non-empty %s", MainFile)
	}
	if !utf8.ValidString(main) {
		return managedSkillPackage{}, fmt.Errorf("%s must be valid UTF-8", MainFile)
	}
	frontmatter, err := parseFrontmatter(main)
	if err != nil {
		return managedSkillPackage{}, fmt.Errorf("parse %s: %w", MainFile, err)
	}
	if err := skillNameValidationError(frontmatter.Name, filepath.Base(resolved)); err != nil {
		return managedSkillPackage{}, err
	}
	description := strings.TrimSpace(frontmatter.Description)
	if description == "" {
		return managedSkillPackage{}, fmt.Errorf("%s description is required", MainFile)
	}
	if utf8.RuneCountInString(description) > 1024 {
		return managedSkillPackage{}, fmt.Errorf("%s description exceeds 1024 characters", MainFile)
	}
	frontmatter.Description = description
	return managedSkillPackage{frontmatter: frontmatter, files: files}, nil
}

type managedSkillDirectory struct {
	absolute string
	relative string
}

func readManagedSkillDirectory(files pkgsandbox.FileAccess, root string) (map[string]string, error) {
	const maxDirectoryEntries = MaxManagedSkillFiles * (maxManagedSkillTreeDepth + 1)

	contents := make(map[string]string)
	pending := []managedSkillDirectory{{absolute: root}}
	visited := 0
	var total int64
	for len(pending) > 0 {
		current := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		entries, err := files.ReadDir(current.absolute)
		if err != nil {
			return nil, err
		}
		slices.SortFunc(entries, func(left, right pkgsandbox.DirEntry) int {
			return strings.Compare(left.Name, right.Name)
		})
		for _, entry := range entries {
			visited++
			if visited > maxDirectoryEntries {
				return nil, ErrSkillLimit
			}
			if entry.Name == "" || entry.Name != path.Base(entry.Name) || entry.Name == "." || entry.Name == ".." || strings.Contains(entry.Name, `\`) {
				return nil, fmt.Errorf("%w: invalid directory entry %q", ErrInvalidSkillRevision, entry.Name)
			}
			relative := entry.Name
			if current.relative != "" {
				relative = path.Join(current.relative, entry.Name)
			}
			if err := validateSkillPath(relative); err != nil {
				return nil, err
			}
			absolute := filepath.Join(current.absolute, entry.Name)
			if entry.IsDir {
				pending = append(pending, managedSkillDirectory{absolute: absolute, relative: relative})
				continue
			}
			if len(contents) >= MaxManagedSkillFiles || entry.Size < 0 || entry.Size > MaxManagedSkillFileBytes {
				return nil, ErrSkillLimit
			}
			content, err := files.ReadFile(absolute)
			if err != nil {
				return nil, err
			}
			if len(content) > MaxManagedSkillFileBytes {
				return nil, ErrSkillLimit
			}
			total += int64(len(content))
			if total > MaxManagedSkillAggregateBytes {
				return nil, ErrSkillLimit
			}
			if _, duplicate := contents[relative]; duplicate {
				return nil, fmt.Errorf("%w: duplicate path %q", ErrInvalidSkillRevision, relative)
			}
			contents[relative] = string(content)
		}
	}
	return contents, nil
}

func boolValue(value *bool) bool {
	return value != nil && *value
}
