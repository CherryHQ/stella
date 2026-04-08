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
func Install(ctx context.Context, source, targetDir string) (string, error) {
	return installSource(ctx, source, targetDir)
}

func installSource(ctx context.Context, source, targetDir string) (string, error) {
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
