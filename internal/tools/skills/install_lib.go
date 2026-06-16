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

	mcpskills "github.com/vaayne/mcphub/pkg/skills"
	_ "github.com/vaayne/mcphub/pkg/skills/providers"

	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

// InstallToStore fetches a skill from source and stores it in the given SkillStore.
// scope must be one of "user", "user_agent", or "system_agent".
// user uses userID; user_agent uses both; system_agent uses agentID.
// Returns the installed skill name on success.
func InstallToStore(ctx context.Context, store pkgplugins.SkillStore, source, scope string, userID string, agentID string) (string, error) {
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

	// Record the install source so the UI can match an installed skill back to its
	// marketplace entry (whose slug may differ from the SKILL.md frontmatter name).
	metaBytes, err := json.Marshal(map[string]string{"created-at": createdAt, "source": source})
	if err != nil {
		return "", fmt.Errorf("encode skill metadata for %q: %w", name, err)
	}

	sk := pkgplugins.Skill{
		Scope:                  scope,
		Name:                   name,
		Description:            fm.Description,
		Status:                 NormalizeSkillStatus(fm.Status),
		DisableModelInvocation: fm.DisableModelInvocation,
		Metadata:               json.RawMessage(metaBytes),
	}
	switch scope {
	case "user":
		sk.UserID = userID
	case "user_agent":
		sk.UserID = userID
		sk.AgentID = agentID
	case "system_agent":
		sk.AgentID = agentID
	}

	if _, err := store.Create(ctx, sk, files); err != nil {
		return "", fmt.Errorf("store skill %q: %w", name, err)
	}

	return name, nil
}

// githubTokenCtxKey carries a GitHub access token used to authenticate clones
// of github.com skill sources.
type githubTokenCtxKey struct{}

// WithGitHubToken returns ctx carrying a GitHub access token used to authenticate
// clones of github.com skill sources. An empty token is a no-op, leaving clones
// anonymous.
func WithGitHubToken(ctx context.Context, token string) context.Context {
	if token == "" {
		return ctx
	}
	return context.WithValue(ctx, githubTokenCtxKey{}, token)
}

func githubTokenFromContext(ctx context.Context) string {
	token, _ := ctx.Value(githubTokenCtxKey{}).(string)
	return token
}

// GitHubSource reports whether source resolves to a github.com repository, which
// can be authenticated with a user's bound GitHub OAuth token via WithGitHubToken.
func GitHubSource(source string) bool {
	if _, _, ok := parseClawhubSource(source); ok {
		return false
	}
	parsed, err := resolveGitSource(source)
	if err != nil {
		return false
	}
	return parsed.Type == mcpskills.SourceTypeGitHub
}

// injectGitHubToken rewrites an https://github.com/... clone URL to embed an
// access token for an authenticated clone. Non-github URLs are returned unchanged.
func injectGitHubToken(gitURL, token string) string {
	const prefix = "https://github.com/"
	if !strings.HasPrefix(gitURL, prefix) {
		return gitURL
	}
	return "https://x-access-token:" + token + "@github.com/" + strings.TrimPrefix(gitURL, prefix)
}

// resolveGitSource parses a non-clawhub source, applying the `#ref` shorthand
// that mcphub's ParseSource does not handle for non-URL inputs.
func resolveGitSource(source string) (*mcpskills.ParsedSource, error) {
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
		return nil, fmt.Errorf("invalid source %q: %w", source, err)
	}
	if ref != "" && parsed.Ref == "" {
		parsed.Ref = ref
	}
	return parsed, nil
}

const githubAuthHint = "GitHub clone failed — if this repository is private or you hit GitHub's anonymous rate limit, connect your GitHub account and retry"

// FetchSkillFiles resolves source, finds the skill directory, and returns the
// skill name and a map of file paths (relative to the skill root) → content.
// cleanup is a no-op for git sources (their path is a shared cache — do NOT
// delete it). For local sources it is also a no-op because the path is the
// user's local directory.
//
// For github.com sources, a token carried via WithGitHubToken is embedded in the
// clone URL to authenticate private repos and avoid anonymous rate limits.
//
// Supported source formats:
//   - clawhub:<slug>[@version]    — download from clawhub.ai
//   - owner/repo@skill-name       — GitHub shorthand (via mcphub)
//   - GitHub/GitLab URLs          — cloned via git
//   - local paths                 — read from filesystem
func FetchSkillFiles(ctx context.Context, source string) (skillName string, files map[string]string, cleanup func(), err error) {
	// Handle clawhub: prefix before any other parsing.
	if slug, version, ok := parseClawhubSource(source); ok {
		return clawhubFetchSkillFiles(ctx, slug, version)
	}

	parsed, err := resolveGitSource(source)
	if err != nil {
		return "", nil, nil, err
	}

	var skillDir string
	switch parsed.Type {
	case mcpskills.SourceTypeGitHub, mcpskills.SourceTypeGitLab, mcpskills.SourceTypeGit:
		cloneURL := parsed.URL
		token := githubTokenFromContext(ctx)
		if token != "" && parsed.Type == mcpskills.SourceTypeGitHub {
			cloneURL = injectGitHubToken(parsed.URL, token)
		}
		src := mcpskills.GitSource{
			URL:         cloneURL,
			Ref:         parsed.Ref,
			Subpath:     parsed.Subpath,
			SkillFilter: parsed.SkillFilter,
		}
		local, ferr := mcpskills.FetchGitSkill(ctx, src)
		if ferr != nil {
			if parsed.Type == mcpskills.SourceTypeGitHub && token == "" {
				return "", nil, nil, fmt.Errorf("%s: %w", githubAuthHint, ferr)
			}
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
