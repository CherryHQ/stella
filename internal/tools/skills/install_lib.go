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

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
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

// fetchAuthedGitHubSkill clones a github.com repo into a private temp directory
// using token-based auth, then locates the skill directory. Unlike the anonymous
// path it does NOT use mcphub's shared on-disk cache: the cache is keyed only by
// owner/repo, so a private repo cached for one user would otherwise be readable by
// any other user, and go-git persists the auth in the cached remote. Passing the
// token via BasicAuth (not the URL) keeps it out of any persisted git config. The
// returned cleanup removes the temp directory and must be called by the caller.
func fetchAuthedGitHubSkill(ctx context.Context, parsed *mcpskills.ParsedSource, token string) (skillDir string, cleanup func(), err error) {
	tmp, err := os.MkdirTemp("", "stella-skill-")
	if err != nil {
		return "", nil, fmt.Errorf("create temp dir: %w", err)
	}
	cleanup = func() { _ = os.RemoveAll(tmp) }

	opts := &git.CloneOptions{
		URL:   parsed.URL,
		Depth: 1,
		Auth:  &githttp.BasicAuth{Username: "x-access-token", Password: token},
	}
	if parsed.Ref != "" {
		opts.ReferenceName = plumbing.NewBranchReferenceName(parsed.Ref)
		opts.SingleBranch = true
	}
	if _, cerr := git.PlainCloneContext(ctx, tmp, false, opts); cerr != nil {
		cleanup()
		return "", nil, cerr
	}

	searchDir := tmp
	if parsed.Subpath != "" {
		searchDir = filepath.Join(tmp, parsed.Subpath)
	}
	dir, ferr := mcpskills.FindSkillDir(searchDir, parsed.SkillFilter)
	if ferr != nil {
		cleanup()
		return "", nil, fmt.Errorf("find skill: %w", ferr)
	}
	return dir, cleanup, nil
}

// githubAuthHint returns user-facing guidance for a failed github.com clone,
// tailored to whether the user has GitHub connected.
func githubAuthHint(hasToken bool) string {
	if hasToken {
		return "GitHub clone failed — check that your connected GitHub account has access to this repository, or reconnect GitHub and retry"
	}
	return "GitHub clone failed — if this repository is private or you hit GitHub's anonymous rate limit, connect your GitHub account and retry"
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

// FetchSkillFiles resolves source, finds the skill directory, and returns the
// skill name and a map of file paths (relative to the skill root) → content.
// cleanup is a no-op for git sources (their path is a shared cache — do NOT
// delete it). For local sources it is also a no-op because the path is the
// user's local directory.
//
// For github.com sources, a token carried via WithGitHubToken authenticates the
// clone (via BasicAuth into a private temp dir) so private repos install and
// anonymous rate limits are avoided.
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

	// tempCleanup removes the authenticated GitHub temp clone (nil for shared-cache
	// and local sources, which own their path). The guard releases it whenever we
	// return an error after the clone; on success the caller takes ownership. It is
	// kept separate from the named cleanup return so the guard never dereferences a
	// return value that an error path has just set to nil.
	var tempCleanup func()
	defer func() {
		if err != nil && tempCleanup != nil {
			tempCleanup()
		}
	}()

	var skillDir string
	switch parsed.Type {
	case mcpskills.SourceTypeGitHub, mcpskills.SourceTypeGitLab, mcpskills.SourceTypeGit:
		token := githubTokenFromContext(ctx)
		if parsed.Type == mcpskills.SourceTypeGitHub && token != "" {
			dir, clean, ferr := fetchAuthedGitHubSkill(ctx, parsed, token)
			if ferr != nil {
				return "", nil, nil, fmt.Errorf("%s: %w", githubAuthHint(true), ferr)
			}
			skillDir = dir
			tempCleanup = clean
		} else {
			local, ferr := mcpskills.FetchGitSkill(ctx, mcpskills.GitSource{
				URL:         parsed.URL,
				Ref:         parsed.Ref,
				Subpath:     parsed.Subpath,
				SkillFilter: parsed.SkillFilter,
			})
			if ferr != nil {
				if parsed.Type == mcpskills.SourceTypeGitHub {
					return "", nil, nil, fmt.Errorf("%s: %w", githubAuthHint(false), ferr)
				}
				return "", nil, nil, fmt.Errorf("fetch skill: %w", ferr)
			}
			skillDir = local.Path
		}
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

	cleanup = tempCleanup
	if cleanup == nil {
		cleanup = func() {}
	}
	return skillName, files, cleanup, nil
}
