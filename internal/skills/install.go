package skills

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	mcpskills "github.com/vaayne/mcphub/pkg/skills"
	_ "github.com/vaayne/mcphub/pkg/skills/providers"
)

// Install fetches a skill from source and installs it into targetDir.
// Source formats:
//   - "owner/repo@skill-name" or "owner/repo@skill-name#ref" (GitHub shorthand)
//   - "https://github.com/owner/repo/tree/branch/path" (GitHub URL)
//   - "https://gitlab.com/owner/repo" (GitLab URL)
//   - "./local/path" (local directory)
//   - "https://example.com/SKILL.md" (direct URL)
//
// Returns the installed skill name.
func Install(ctx context.Context, source, targetDir string) (string, error) {
	return install(ctx, source, targetDir)
}

func (t *SkillsTool) install(ctx context.Context, args map[string]any) (string, error) {
	source, _ := args["source"].(string)
	if source == "" {
		return "", fmt.Errorf("source is required for install action (e.g. owner/repo@skill-name)")
	}

	targetDir := filepath.Join(t.workspace, "skills")
	skillName, err := install(ctx, source, targetDir)
	if err != nil {
		return "", err
	}

	installed := filepath.Join(targetDir, skillName)
	return fmt.Sprintf("Skill %q installed to %s.", skillName, installed), nil
}

// install is the shared implementation for both the public Install function and the tool method.
func install(ctx context.Context, source, targetDir string) (string, error) {
	// Handle anna's #ref syntax in shorthand: owner/repo@skill#ref
	// mcphub's ParseSource doesn't split #ref from shorthand, so we pre-process it.
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
		return "", fmt.Errorf("invalid source %q: %w", source, err)
	}

	// Apply pre-processed ref if ParseSource didn't capture one.
	if ref != "" && parsed.Ref == "" {
		parsed.Ref = ref
	}

	switch parsed.Type {
	case mcpskills.SourceTypeGitHub, mcpskills.SourceTypeGitLab, mcpskills.SourceTypeGit:
		return installFromGit(ctx, parsed, targetDir)
	case mcpskills.SourceTypeLocal:
		return installFromLocal(parsed, targetDir)
	case mcpskills.SourceTypeDirectURL, mcpskills.SourceTypeWellKnown:
		return "", fmt.Errorf("source type %q is not yet supported", parsed.Type)
	default:
		return "", fmt.Errorf("unknown source type %q", parsed.Type)
	}
}

func installFromGit(ctx context.Context, parsed *mcpskills.ParsedSource, targetDir string) (string, error) {
	src := mcpskills.GitSource{
		URL:         parsed.URL,
		Ref:         parsed.Ref,
		Subpath:     parsed.Subpath,
		SkillFilter: parsed.SkillFilter,
	}

	local, err := mcpskills.FetchGitSkill(ctx, src)
	if err != nil {
		return "", fmt.Errorf("fetch skill: %w", err)
	}

	dst := filepath.Join(targetDir, local.SkillName)
	if err := mcpskills.InstallSkill(local.Path, dst); err != nil {
		return "", fmt.Errorf("install skill: %w", err)
	}

	return local.SkillName, nil
}

func installFromLocal(parsed *mcpskills.ParsedSource, targetDir string) (string, error) {
	skillDir, err := mcpskills.FindSkillDir(parsed.LocalPath, "")
	if err != nil {
		return "", fmt.Errorf("find skill: %w", err)
	}

	skillName := filepath.Base(skillDir)
	dst := filepath.Join(targetDir, skillName)
	if err := mcpskills.InstallSkill(skillDir, dst); err != nil {
		return "", fmt.Errorf("install skill: %w", err)
	}

	return skillName, nil
}
