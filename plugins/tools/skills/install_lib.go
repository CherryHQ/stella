package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	pkgplugins "github.com/vaayne/anna/pkg/plugins"
	mcpskills "github.com/vaayne/mcphub/pkg/skills"
	_ "github.com/vaayne/mcphub/pkg/skills/providers"
)

// InstallToStore fetches a skill from source and stores it in the given SkillStore.
// scope must be one of "user", "project", or "agent".
// For scope="user", userID is used; for scope="project", project is used.
// Returns the installed skill name on success.
func InstallToStore(ctx context.Context, store pkgplugins.SkillStore, source, scope string, userID int64, project string) (string, error) {
	skillName, files, cleanup, err := FetchSkillFiles(ctx, source)
	if err != nil {
		return "", err
	}
	defer cleanup()

	mainContent, ok := files[pkgplugins.SkillMainFile]
	if !ok {
		return "", fmt.Errorf("fetched skill %q is missing SKILL.md", skillName)
	}

	fm, err := parseFrontmatter(mainContent)
	if err != nil {
		return "", fmt.Errorf("parse SKILL.md for %q: %w", skillName, err)
	}

	name := fm.Name
	if name == "" {
		name = skillName
	}

	createdAt := fm.CreatedAt
	if createdAt == "" {
		createdAt = time.Now().UTC().Format(time.RFC3339)
	}

	metaJSON := fmt.Sprintf(`{"created-at":%q}`, createdAt)

	sk := pkgplugins.Skill{
		Scope:                  scope,
		Name:                   name,
		Description:            fm.Description,
		Status:                 NormalizeSkillStatus(fm.Status),
		DisableModelInvocation: fm.DisableModelInvocation,
		Metadata:               json.RawMessage(metaJSON),
	}
	switch scope {
	case "user":
		sk.UserID = userID
	case "project":
		sk.Project = project
	}

	if _, err := store.Create(ctx, sk, files); err != nil {
		return "", fmt.Errorf("store skill %q: %w", name, err)
	}

	return name, nil
}

// FetchSkillFiles resolves source, finds the skill directory, and returns the
// skill name and a map of file paths (relative to the skill root) → content.
// cleanup is a no-op for git sources (their path is a shared cache — do NOT
// delete it). For local sources it is also a no-op because the path is the
// user's local directory.
func FetchSkillFiles(ctx context.Context, source string) (skillName string, files map[string]string, cleanup func(), err error) {
	ref := ""
	if !strings.HasPrefix(source, "http://") && !strings.HasPrefix(source, "https://") &&
		!strings.HasPrefix(source, "/") && !strings.HasPrefix(source, ".") {
		if idx := strings.LastIndex(source, "#"); idx != -1 {
			ref = source[idx+1:]
			source = source[:idx]
		}
	}

	parsed, err := mcpskills.ParseSource(source)
	if err != nil {
		return "", nil, nil, fmt.Errorf("invalid source %q: %w", source, err)
	}
	if ref != "" && parsed.Ref == "" {
		parsed.Ref = ref
	}

	var skillDir string
	switch parsed.Type {
	case mcpskills.SourceTypeGitHub, mcpskills.SourceTypeGitLab, mcpskills.SourceTypeGit:
		src := mcpskills.GitSource{
			URL:         parsed.URL,
			Ref:         parsed.Ref,
			Subpath:     parsed.Subpath,
			SkillFilter: parsed.SkillFilter,
		}
		local, ferr := mcpskills.FetchGitSkill(ctx, src)
		if ferr != nil {
			return "", nil, nil, fmt.Errorf("fetch skill: %w", ferr)
		}
		skillDir = local.Path
	case mcpskills.SourceTypeLocal:
		dir, ferr := mcpskills.FindSkillDir(parsed.LocalPath, "")
		if ferr != nil {
			return "", nil, nil, fmt.Errorf("find skill: %w", ferr)
		}
		skillDir = dir
	case mcpskills.SourceTypeDirectURL, mcpskills.SourceTypeWellKnown:
		return "", nil, nil, fmt.Errorf("source type %q is not yet supported", parsed.Type)
	default:
		return "", nil, nil, fmt.Errorf("unknown source type %q", parsed.Type)
	}

	skillName = filepath.Base(skillDir)

	// Walk the skill directory and collect all regular files.
	files = make(map[string]string)
	if werr := filepath.WalkDir(skillDir, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(skillDir, path)
		if rerr != nil {
			return rerr
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		files[rel] = string(data)
		return nil
	}); werr != nil {
		return "", nil, nil, fmt.Errorf("walk skill dir: %w", werr)
	}

	return skillName, files, func() {}, nil
}
