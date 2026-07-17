package skills

import (
	"context"
	"encoding/json"
	"errors"
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

// InstallToStore fetches a skill and commits it through the internal Store.
// scope must be one of "user", "user_agent", or "system_agent".
// user uses userID; user_agent uses both; system_agent uses agentID.
func InstallToStore(ctx context.Context, store Store, source, scope string, userID string, agentID string) (SkillSnapshot, error) {
	skillName, files, version, cleanup, err := FetchSkillFiles(ctx, source)
	if err != nil {
		return SkillSnapshot{}, err
	}
	defer cleanup()

	mainContent, ok := files[MainFile]
	if !ok {
		return SkillSnapshot{}, fmt.Errorf("fetched skill %q is missing SKILL.md", skillName)
	}

	fm, err := parseFrontmatter(mainContent)
	if err != nil {
		return SkillSnapshot{}, fmt.Errorf("parse SKILL.md for %q: %w", skillName, err)
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
	// marketplace entry (whose slug may differ from the SKILL.md frontmatter name),
	// plus the resolved version (git ref/commit or clawhub version) for display.
	meta := map[string]string{"created-at": createdAt, "source": source}
	if version != "" {
		meta["version"] = version
	}
	metaBytes, err := json.Marshal(meta)
	if err != nil {
		return SkillSnapshot{}, fmt.Errorf("encode skill metadata for %q: %w", name, err)
	}

	sk := Skill{
		Scope:                  scope,
		Name:                   name,
		Description:            fm.Description,
		Status:                 SkillStatusActive,
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

	snapshot, err := store.CreateManagedSkill(ctx, sk, files)
	if err != nil {
		return SkillSnapshot{}, fmt.Errorf("store skill %q: %w", name, err)
	}
	return snapshot, nil
}

// ErrNoUpgradeSource indicates a skill has no recorded install source to re-fetch
// from (e.g. it was created by hand rather than installed).
var ErrNoUpgradeSource = errors.New("skill was not installed from a source")

// UpgradeResult reports the outcome of re-fetching a skill from its source.
type UpgradeResult struct {
	Updated         bool   // true when the skill's files/version changed
	Version         string // the version now installed
	PreviousVersion string // the version before the upgrade
}

// UpgradeInStore re-fetches the skill identified by skillID from the install
// source recorded in its metadata and, when the resolved version differs from the
// stored one, replaces the skill's files and refreshes its metadata from the new
// SKILL.md. An unchanged version is a no-op (Updated=false), so this doubles as a
// check: callers get "already up to date" without a second round-trip.
//
// For github.com sources a token carried via WithGitHubToken authenticates the
// fetch, exactly as during install. metadata is the skill's current metadata blob;
// its created-at and source are preserved while version is bumped.
func UpgradeInStore(ctx context.Context, store pkgplugins.SkillStore, skillID string, metadata json.RawMessage) (UpgradeResult, error) {
	meta := map[string]any{}
	if len(metadata) > 0 {
		_ = json.Unmarshal(metadata, &meta)
	}
	source, _ := meta["source"].(string)
	if source == "" {
		return UpgradeResult{}, ErrNoUpgradeSource
	}
	currentVersion, _ := meta["version"].(string)

	name, files, version, cleanup, err := FetchSkillFiles(ctx, source)
	if err != nil {
		return UpgradeResult{}, err
	}
	defer cleanup()

	// A version we cannot tell apart from the installed one means nothing to do.
	// Pinned tags resolve to themselves, so this is also how "pinned = frozen" falls out.
	if version == currentVersion {
		return UpgradeResult{Updated: false, Version: version}, nil
	}

	mainContent, ok := files[pkgplugins.SkillMainFile]
	if !ok {
		return UpgradeResult{}, fmt.Errorf("fetched skill %q is missing SKILL.md", name)
	}
	fm, err := parseFrontmatter(mainContent)
	if err != nil {
		return UpgradeResult{}, fmt.Errorf("parse SKILL.md for %q: %w", name, err)
	}

	existing, err := store.ListFiles(ctx, skillID)
	if err != nil {
		return UpgradeResult{}, fmt.Errorf("list installed files: %w", err)
	}
	// Write the new files before pruning stale ones (and bump metadata last) so a
	// mid-upgrade failure leaves a still-loadable skill — old files plus whatever
	// new files landed — rather than a half-deleted one. This is not atomic; a
	// transactional store method would be the complete fix.
	for path, content := range files {
		if err := store.UpsertFile(ctx, skillID, path, content); err != nil {
			return UpgradeResult{}, fmt.Errorf("write file %q: %w", path, err)
		}
	}
	keep := make(map[string]bool, len(files))
	for path := range files {
		keep[path] = true
	}
	for _, path := range existing {
		if !keep[path] {
			if err := store.DeleteFile(ctx, skillID, path); err != nil {
				return UpgradeResult{}, fmt.Errorf("remove stale file %q: %w", path, err)
			}
		}
	}

	if version != "" {
		meta["version"] = version
	} else {
		delete(meta, "version")
	}
	metaBytes, err := json.Marshal(meta)
	if err != nil {
		return UpgradeResult{}, fmt.Errorf("encode skill metadata: %w", err)
	}
	disable := fm.DisableModelInvocation
	if err := store.Update(ctx, skillID, pkgplugins.SkillUpdatePatch{
		Description:            &fm.Description,
		DisableModelInvocation: &disable,
		Metadata:               json.RawMessage(metaBytes),
	}); err != nil {
		return UpgradeResult{}, fmt.Errorf("update skill %q: %w", name, err)
	}

	return UpgradeResult{Updated: true, Version: version, PreviousVersion: currentVersion}, nil
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

	if cerr := cloneGitHubRef(ctx, tmp, parsed.URL, parsed.Ref, token); cerr != nil {
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

// cloneGitHubRef shallow-clones url into dest using token-based BasicAuth. When
// ref is set it is tried as a branch and then as a tag, since go-git needs a
// fully-qualified ref and the user's free-text version may be either; dest is
// wiped between attempts because PlainClone requires an empty target. An empty
// ref clones the default branch.
func cloneGitHubRef(ctx context.Context, dest, url, ref, token string) error {
	auth := &githttp.BasicAuth{Username: "x-access-token", Password: token}
	refNames := []plumbing.ReferenceName{""}
	if ref != "" {
		refNames = []plumbing.ReferenceName{
			plumbing.NewBranchReferenceName(ref),
			plumbing.NewTagReferenceName(ref),
		}
	}
	var err error
	for i, rn := range refNames {
		if i > 0 {
			if rerr := resetDir(dest); rerr != nil {
				return rerr
			}
		}
		opts := &git.CloneOptions{URL: url, Depth: 1, Auth: auth}
		if rn != "" {
			opts.ReferenceName = rn
			opts.SingleBranch = true
		}
		if _, err = git.PlainCloneContext(ctx, dest, false, opts); err == nil {
			return nil
		}
	}
	return err
}

// resetDir empties dir (a failed clone leaves a partial .git behind), recreating
// it so the next PlainClone sees an empty target.
func resetDir(dir string) error {
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	return os.MkdirAll(dir, 0o700)
}

// resolveGitVersion returns the version to record for a git-sourced skill: the
// short HEAD commit the working tree resolved to. Recording the commit — not the
// pinned ref — is what makes upgrades work: a branch pin re-fetched later resolves
// to a new commit and is detected as an update, while a tag pin always resolves to
// the same commit and correctly reports "up to date". The human-readable ref stays
// visible in the skill's source. Best-effort — returns "" if the commit cannot be
// read. skillDir may be a subdirectory of the repo; DetectDotGit walks up to the
// enclosing .git.
func resolveGitVersion(skillDir string) string {
	repo, err := git.PlainOpenWithOptions(skillDir, &git.PlainOpenOptions{DetectDotGit: true})
	if err != nil {
		return ""
	}
	head, err := repo.Head()
	if err != nil {
		return ""
	}
	return head.Hash().String()[:7]
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
// skill name, a map of file paths (relative to the skill root) → content, and
// the installed version (the git ref/commit for git sources, the clawhub version
// for clawhub sources, empty for local). cleanup is a no-op for shared-cache git
// and local sources; for an authenticated GitHub clone it removes the temp dir.
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
func FetchSkillFiles(ctx context.Context, source string) (skillName string, files map[string]string, version string, cleanup func(), err error) {
	// Handle clawhub: prefix before any other parsing.
	if slug, ver, ok := parseClawhubSource(source); ok {
		name, f, clean, cerr := clawhubFetchSkillFiles(ctx, slug, ver)
		return name, f, ver, clean, cerr
	}

	parsed, err := resolveGitSource(source)
	if err != nil {
		return "", nil, "", nil, err
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
				return "", nil, "", nil, fmt.Errorf("%s: %w", githubAuthHint(true), ferr)
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
					return "", nil, "", nil, fmt.Errorf("%s: %w", githubAuthHint(false), ferr)
				}
				return "", nil, "", nil, fmt.Errorf("fetch skill: %w", ferr)
			}
			skillDir = local.Path
		}
		version = resolveGitVersion(skillDir)
	case mcpskills.SourceTypeLocal:
		dir, ferr := mcpskills.FindSkillDir(parsed.LocalPath, "")
		if ferr != nil {
			return "", nil, "", nil, fmt.Errorf("find skill: %w", ferr)
		}
		skillDir = dir
	case mcpskills.SourceTypeDirectURL, mcpskills.SourceTypeWellKnown:
		return "", nil, "", nil, fmt.Errorf("source type %q is not yet supported", parsed.Type)
	default:
		return "", nil, "", nil, fmt.Errorf("unknown source type %q", parsed.Type)
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
		files[filepath.ToSlash(rel)] = string(data)
		return nil
	}); werr != nil {
		return "", nil, "", nil, fmt.Errorf("walk skill dir: %w", werr)
	}

	cleanup = tempCleanup
	if cleanup == nil {
		cleanup = func() {}
	}
	return skillName, files, version, cleanup, nil
}
