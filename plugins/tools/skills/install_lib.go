package skills

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	mcpskills "github.com/vaayne/mcphub/pkg/skills"
	_ "github.com/vaayne/mcphub/pkg/skills/providers"
)

// Install fetches a skill from source and installs it into targetDir.
// This is a filesystem-based compatibility shim used by the CLI.
// The tool layer uses FetchSkillFiles + store.Create instead.
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
