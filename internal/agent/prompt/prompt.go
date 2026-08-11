package prompt

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"io"
	"io/fs"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"text/template"

	"github.com/CherryHQ/stella/internal/home"
	"github.com/CherryHQ/stella/internal/memory"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	"github.com/CherryHQ/stella/pkg/sandbox"
	"github.com/CherryHQ/stella/resources"
)

type SectionsBuilder func(ctx context.Context, build pkgplugins.SystemPromptContext) ([]pkgplugins.SystemPromptSection, error)

type ContextRoot interface {
	Close() error
	Stat(context.Context, string) (fs.FileInfo, error)
	Read(context.Context, string, io.Writer, home.ReadOptions) error
}

// ProjectContext is a bounded immutable snapshot of project instruction files.
// Its contents are captured while an authorized Home root capability is open.
type ProjectContext struct {
	files  []contextFile
	loaded bool
}

//go:embed template/system_prompt.tmpl
var systemTemplate string

//go:embed template/system.md
var defaultSystemPrompt string

// DefaultSystemPrompt returns the default system prompt text.
func DefaultSystemPrompt() string { return strings.TrimSpace(defaultSystemPrompt) }

const guestLimitations = `You are serving an unauthenticated guest. You may converse using only the visible conversation history. You have no tools, files, workspace, skills, plugins, memories, profile, reflection, delegation, secrets, OAuth connections, or other Stella capabilities. Do not claim to access or retain anything beyond this conversation.`

// BuildGuestSystemPrompt returns the deliberately minimal guest prompt. The
// configured agent base prompt is operator-visible and is the only agent
// customization guests receive.
func BuildGuestSystemPrompt(base string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		base = "You are a conversational assistant."
	}
	return base + "\n\n# Guest limitations\n\n" + guestLimitations
}

// DefaultAgentSoul returns the first soul tagged "default" from the builtin
// registry, used as the fallback persona when an agent has no override in memory.
func DefaultAgentSoul() string {
	reg, err := resources.Default()
	if err != nil {
		return ""
	}
	for _, res := range reg.List(resources.KindSoul) {
		if slices.Contains(res.Tags, "default") {
			return strings.TrimSpace(res.Content)
		}
	}
	return ""
}

var systemTmpl = template.Must(template.New("system").Funcs(template.FuncMap{
	"escapeXML": escapeXML,
}).Parse(systemTemplate))

// contextFile represents a discovered AGENTS.md file with its path and content.
type contextFile struct {
	Path    string
	Content string
}

// promptData holds all pre-computed data for the system prompt template.
type promptData struct {
	SystemPrompt   string // agent's base system prompt from DB
	AgentSoul      string // per-user agent soul from ProfileStore
	UserProfile    string // per-user profile from ProfileStore
	ProfileEntries []memory.ProfileEntry
	GroupMemory    string // group-scoped shared memory (non-empty only for group sessions)
	Constraints    []memory.ConstraintEntry
	PluginPrompts  []pkgplugins.SystemPromptSection
	PromptSections []pkgplugins.SystemPromptSection
	ContextFiles   []contextFile // AGENTS.md files (root → leaf)

	// Group session rendering. IsGroup switches the template from the per-user
	// "## User Profile" section to "## Group Memory" (+ optional current speaker),
	// driven by session kind, not by whether group memory text is non-empty.
	IsGroup bool
}

// DBPromptParams holds the parameters for building a system prompt from DB-backed config.
type DBPromptParams struct {
	SystemPrompt   string          // agent's base system prompt from DB
	AgentSoul      string          // agent's default soul from DB (fallback for all users)
	Memory         memory.Provider // active provider for profile loading (may be nil)
	UserID         string          // auth user ID for profile lookup
	AgentID        string          // agent ID for profile lookup
	GroupID        string          // group ID for group memory lookup (D4); mutually exclusive with UserID
	GroupMemory    string          // pre-loaded group memory content; injected when non-empty
	StellaHome     string
	AgentRoot      string
	ProjectRoot    string // optional project root for local/project-attached runs
	UserRoot       string // per-user writable root
	Sections       []pkgplugins.SystemPromptSection
	Host           sandbox.Host
	ProjectContext ProjectContext
	// nil means current memory; non-nil values, including zero, are frozen snapshots.
	SnapshotVersion *int64

	// CurrentSpeaker is retained for compatibility with callers/tests that still
	// populate it, but it is intentionally not rendered into the system prompt.
	// Runtime injects speaker metadata as per-turn message context so reused group
	// runners keep a stable system prefix for prompt caching.
	CurrentSpeaker    memory.CurrentSpeaker
	HasCurrentSpeaker bool
}

// BuildSystemPromptFromDB composes the full system prompt by populating a
// promptData struct and executing the system template.
//
// Layers:
//  1. System prompt — the agent's base system prompt from DB
//  2. Tools — always-available tool descriptions (in the template)
//  3. Agent soul — per-user identity/personality from memory ProfileStore
//  4. User profile — per-user facts/context from memory ProfileStore
//  5. Extension prompt sections — stable prompt content owned by plugins
//  6. Project context — AGENTS.md files from cwd ancestors
func BuildSystemPromptFromDB(ctx context.Context, p DBPromptParams) string {
	sysPrompt := strings.TrimSpace(p.SystemPrompt)
	if sysPrompt == "" {
		sysPrompt = strings.TrimSpace(defaultSystemPrompt)
	}

	// Soul priority: per-user override > agent default > global builtin default.
	agentSoul := strings.TrimSpace(p.AgentSoul)
	if agentSoul == "" {
		agentSoul = DefaultAgentSoul()
	}
	data := promptData{
		SystemPrompt: sysPrompt,
		AgentSoul:    agentSoul,
	}

	// Memory: per-user soul overrides the agent default when set.
	if p.UserID != "" && p.AgentID != "" {
		if p.SnapshotVersion != nil {
			version := *p.SnapshotVersion
			// Versioned reads: use frozen version for stable session identity.
			if vps, ok := p.Memory.(memory.VersionedProfileStore); ok {
				if s, err := vps.GetAgentSoulAt(ctx, p.UserID, p.AgentID, version); err == nil {
					if soul := strings.TrimRight(s, "\n"); soul != "" {
						data.AgentSoul = soul
					}
				}
				if c, err := vps.GetProfileAt(ctx, p.UserID, p.AgentID, version); err == nil {
					data.UserProfile = strings.TrimRight(c, "\n")
				}
			}
			if vcs, ok := p.Memory.(memory.VersionedConstraintStore); ok {
				if constraints, err := vcs.GetConstraintsAt(ctx, p.UserID, p.AgentID, version); err == nil {
					data.Constraints = constraints
				}
			}
		} else {
			// Current reads: standard behavior when no session snapshot exists.
			if ps, ok := p.Memory.(memory.ProfileStore); ok {
				if s, err := ps.GetAgentSoul(ctx, p.UserID, p.AgentID); err == nil {
					if soul := strings.TrimRight(s, "\n"); soul != "" {
						data.AgentSoul = soul
					}
				}
				if c, err := ps.GetProfile(ctx, p.UserID, p.AgentID); err == nil {
					data.UserProfile = strings.TrimRight(c, "\n")
				}
			}
			if cs, ok := p.Memory.(memory.ConstraintStore); ok {
				if constraints, err := cs.GetConstraints(ctx, p.UserID, p.AgentID); err == nil {
					data.Constraints = constraints
				}
			}
		}
		// Profile entries: auto-generated dated entries from D6 ingest.
		if pes, ok := p.Memory.(memory.ProfileEntryStore); ok {
			if entries, err := pes.GetProfileEntries(ctx, p.UserID, p.AgentID); err == nil {
				data.ProfileEntries = entries
			}
		}
	}

	// Group memory: inject shared group knowledge for group sessions (D4).
	// Group mode is keyed on GroupID, not on group memory being non-empty, so a
	// group turn never falls through to the per-user "## User Profile" section.
	data.IsGroup = p.GroupID != ""
	if p.GroupID != "" && p.Memory != nil {
		if gms, ok := p.Memory.(memory.GroupMemoryStore); ok {
			if content, err := gms.GetGroupMemory(ctx, p.GroupID); err == nil && content != "" {
				data.GroupMemory = strings.TrimRight(content, "\n")
			}
		}
	}

	// Current speaker (D9): intentionally not rendered into the system prompt.
	// Runtime injects it as per-turn message context instead, preserving the group
	// system prompt prefix across speakers for provider prompt caches.
	// World facts are deliberately search-first: they remain available through
	// memory.search under the same snapshot/version semantics, but are
	// not injected into every prompt by default.

	for _, s := range p.Sections {
		if s.Inline {
			data.PluginPrompts = append(data.PluginPrompts, s)
		} else {
			data.PromptSections = append(data.PromptSections, s)
		}
	}

	// Project context.
	if p.ProjectContext.loaded {
		data.ContextFiles = p.ProjectContext.files
	} else {
		contextHost, closeContextHost := resolvePromptContextHost(ctx, p.Host, p.ProjectRoot)
		defer closeContextHost()
		data.ContextFiles = loadProjectContextFiles(contextHost, p.ProjectRoot)
	}

	var buf bytes.Buffer
	_ = systemTmpl.Execute(&buf, data)
	return buf.String()
}

// SnapshotProjectContext reads bounded context through an authorized root and
// always closes it before returning, so owner deletion is fenced only during
// active filesystem I/O rather than memory reads or template rendering.
func SnapshotProjectContext(ctx context.Context, root ContextRoot, projectPath string) (snapshot ProjectContext, resultErr error) {
	if root == nil {
		return ProjectContext{}, errors.New("prompt: project context root is required")
	}
	defer func() { resultErr = errors.Join(resultErr, root.Close()) }()
	return ReadProjectContext(ctx, root, projectPath)
}

// ReadProjectContext snapshots bounded context without taking ownership of root.
// A caller combining several project snapshots can therefore hold one owner gate
// across all reads and close it before any plugin or template work begins.
func ReadProjectContext(ctx context.Context, root ContextRoot, projectPath string) (ProjectContext, error) {
	if root == nil {
		return ProjectContext{}, errors.New("prompt: project context root is required")
	}
	return ProjectContext{files: loadRootProjectContextFiles(ctx, root, projectPath), loaded: true}, nil
}

const (
	promptContextMaxBytes      = int64(256 * 1024)
	promptContextTotalMaxBytes = int64(512 * 1024)
	promptContextMaxFiles      = 32
)

func loadRootProjectContextFiles(ctx context.Context, root ContextRoot, projectPath string) []contextFile {
	if projectPath == "" {
		projectPath = "."
	}
	if projectPath != "." && (path.IsAbs(projectPath) || path.Clean(projectPath) != projectPath) {
		return nil
	}
	directories := []string{"."}
	if projectPath != "." {
		current := ""
		for segment := range strings.SplitSeq(projectPath, "/") {
			if segment == "" || segment == "." || segment == ".." {
				return nil
			}
			current = path.Join(current, segment)
			directories = append(directories, current)
			if len(directories) > promptContextMaxFiles {
				return nil
			}
		}
	}
	files := make([]contextFile, 0, len(directories))
	remaining := promptContextTotalMaxBytes
	for _, directory := range directories {
		if remaining == 0 {
			break
		}
		name := path.Join(directory, "AGENTS.md")
		info, err := root.Stat(ctx, name)
		if err != nil || !info.Mode().IsRegular() || info.Size() > promptContextMaxBytes || info.Size() > remaining {
			continue
		}
		var content bytes.Buffer
		limit := min(promptContextMaxBytes, remaining)
		if err := root.Read(ctx, name, &content, home.ReadOptions{MaxBytes: limit}); err != nil {
			if errors.Is(err, home.ErrReadLimit) {
				continue
			}
			continue
		}
		files = append(files, contextFile{Path: name, Content: content.String()})
		remaining -= int64(content.Len())
	}
	return files
}

// resolvePromptContextHost returns the host to use for reading prompt context
// files. When a session host is already available it is used directly. When no
// host is present (prompt rendering outside of a runner session) the function
// returns nil and host.go falls back to plain os.* calls.
func resolvePromptContextHost(_ context.Context, host sandbox.Host, _ string) (sandbox.Host, func()) {
	return host, func() {}
}

func loadProjectContextFiles(host sandbox.Host, cwd string) []contextFile {
	if cwd == "" {
		return nil
	}

	absDir, err := filepath.Abs(cwd)
	if err != nil {
		return nil
	}

	var files []contextFile
	seen := map[string]bool{}

	for {
		if path := resolveFile(host, absDir, "AGENTS.md"); path != "" {
			if !seen[path] {
				seen[path] = true
				if content, ok := readPromptFile(host, path); ok {
					files = append(files, contextFile{Path: path, Content: content})
				}
			}
		}

		parent := filepath.Dir(absDir)
		if parent == absDir {
			break
		}
		absDir = parent
	}

	// Reverse so ancestors come first (root → leaf).
	for i, j := 0, len(files)-1; i < j; i, j = i+1, j-1 {
		files[i], files[j] = files[j], files[i]
	}

	return files
}

// resolveFile finds a file in dir with case-insensitive matching.
// Returns the full path if found, empty string otherwise.
func resolveFile(host sandbox.Host, dir, name string) string {
	exact := filepath.Join(dir, name)
	if path, ok := statPromptFile(host, exact); ok {
		return path
	}
	entries, err := readPromptDir(host, dir)
	if err != nil {
		return ""
	}
	target := strings.ToLower(name)
	for _, e := range entries {
		if strings.ToLower(e.Name) == target {
			return filepath.Join(dir, e.Name)
		}
	}
	return ""
}

func escapeXML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, `"`, "&quot;")
	s = strings.ReplaceAll(s, "'", "&apos;")
	return s
}
