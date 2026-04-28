package agent

import (
	"bytes"
	"context"
	_ "embed"
	"path/filepath"
	"slices"
	"strings"
	"text/template"

	builtinres "github.com/vaayne/anna/internal/resources"
	"github.com/vaayne/anna/internal/sandbox"
	"github.com/vaayne/anna/pkg/memory"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

//go:embed template/system_prompt.tmpl
var systemTemplate string

//go:embed template/system.md
var defaultSystemPrompt string

// DefaultSystemPrompt returns the default system prompt text.
func DefaultSystemPrompt() string { return strings.TrimSpace(defaultSystemPrompt) }

// DefaultAgentSoul returns the first soul tagged "default" from the builtin
// registry, used as the fallback persona when an agent has no override in memory.
func DefaultAgentSoul() string {
	reg, err := builtinres.Default()
	if err != nil {
		return ""
	}
	for _, res := range reg.List(builtinres.KindSoul) {
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
	Constraints    []memory.ConstraintEntry
	MCPTools       []promptToolEntry // prompt inventory for MCP-discovered tools
	PluginPrompts  []pkgplugins.SystemPromptSection
	PromptSections []pkgplugins.SystemPromptSection
	ContextFiles   []contextFile // AGENTS.md files (root → leaf)
}

type promptToolEntry struct {
	ID          string
	Description string
	ServerName  string
}

// DBPromptParams holds the parameters for building a system prompt from DB-backed config.
type DBPromptParams struct {
	SystemPrompt    string          // agent's base system prompt from DB
	AgentSoul       string          // agent's default soul from DB (fallback for all users)
	Memory          memory.Provider // active provider for profile loading (may be nil)
	UserID          int64           // auth user ID for profile lookup
	AgentID         string          // agent ID for profile lookup
	AnnaHome        string
	AgentRoot       string
	ProjectRoot     string // optional project root for local/project-attached runs
	UserRoot        string // per-user writable root
	PromptTools     []pkgplugins.PromptToolInfo
	PluginPrompts   []pkgplugins.SystemPromptSection
	PromptSections  []pkgplugins.SystemPromptSection
	Host            sandbox.Host
	SnapshotVersion int64 // frozen memory version for this session; 0 means current
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
	if p.UserID > 0 && p.AgentID != "" {
		if p.SnapshotVersion > 0 {
			// Versioned reads: use frozen version for stable session identity.
			if vps, ok := p.Memory.(memory.VersionedProfileStore); ok {
				if s, err := vps.GetAgentSoulAt(ctx, p.UserID, p.AgentID, p.SnapshotVersion); err == nil {
					if soul := strings.TrimRight(s, "\n"); soul != "" {
						data.AgentSoul = soul
					}
				}
				if c, err := vps.GetProfileAt(ctx, p.UserID, p.AgentID, p.SnapshotVersion); err == nil {
					data.UserProfile = strings.TrimRight(c, "\n")
				}
			}
			if vcs, ok := p.Memory.(memory.VersionedConstraintStore); ok {
				if constraints, err := vcs.GetConstraintsAt(ctx, p.UserID, p.AgentID, p.SnapshotVersion); err == nil {
					data.Constraints = constraints
				}
			}
		} else {
			// Current reads: standard behavior (no snapshot, or version 0).
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
	}

	// MCP prompt inventory.
	for _, tool := range p.PromptTools {
		entry := promptToolEntry{ID: tool.Name, Description: tool.Description}
		if serverName, _ := tool.Metadata["server_name"].(string); serverName != "" {
			entry.ServerName = serverName
		}
		data.MCPTools = append(data.MCPTools, entry)
	}

	data.PluginPrompts = append(data.PluginPrompts, p.PluginPrompts...)
	data.PromptSections = append(data.PromptSections, p.PromptSections...)

	// Project context.
	contextHost, closeContextHost := resolvePromptContextHost(ctx, p.Host, p.ProjectRoot)
	defer closeContextHost()
	data.ContextFiles = loadProjectContextFiles(contextHost, p.ProjectRoot)

	var buf bytes.Buffer
	_ = systemTmpl.Execute(&buf, data)
	return buf.String()
}

// resolvePromptContextHost returns the host to use for reading prompt context
// files. When a session host is already available it is used directly. When no
// host is present (prompt rendering outside of a runner session) the function
// returns nil and prompt_host.go falls back to plain os.* calls.
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
