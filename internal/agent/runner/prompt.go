package runner

import (
	"bytes"
	"context"
	_ "embed"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/vaayne/anna/internal/sandbox"
	"github.com/vaayne/anna/pkg/memory"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

//go:embed template/system_prompt.tmpl
var systemTemplate string

//go:embed template/system.md
var defaultSystemPrompt string

//go:embed template/soul.md
var defaultAgentSoul string

// DefaultSystemPrompt returns the default system prompt text.
func DefaultSystemPrompt() string { return strings.TrimSpace(defaultSystemPrompt) }

// DefaultAgentSoul returns the default agent soul text.
func DefaultAgentSoul() string { return strings.TrimSpace(defaultAgentSoul) }

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
	SystemPrompt   string            // agent's base system prompt from DB
	AgentSoul      string            // per-user agent soul from ProfileStore
	UserProfile    string            // per-user profile from ProfileStore
	MCPTools       []promptToolEntry // prompt inventory for MCP-discovered tools
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
	SystemPrompt   string          // agent's full system prompt from DB (the base layer)
	Memory         memory.Provider // active provider for profile loading (may be nil)
	UserID         int64           // auth user ID for profile lookup
	AgentID        string          // agent ID for profile lookup
	AnnaHome       string
	Workspace      string
	Cwd            string // optional working directory
	UserDataDir    string // optional per-user data directory
	PromptTools    []pkgplugins.PromptToolInfo
	PromptSections []pkgplugins.SystemPromptSection
	Host           sandbox.Host
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

	data := promptData{
		SystemPrompt: sysPrompt,
		AgentSoul:    strings.TrimSpace(defaultAgentSoul),
	}

	// Memory: agent soul + user profile (always rendered, populated when available).
	if ps, ok := p.Memory.(memory.ProfileStore); ok && p.UserID > 0 && p.AgentID != "" {
		if s, err := ps.GetAgentSoul(ctx, p.UserID, p.AgentID); err == nil {
			if soul := strings.TrimRight(s, "\n"); soul != "" {
				data.AgentSoul = soul
			}
		}
		if c, err := ps.GetProfile(ctx, p.UserID, p.AgentID); err == nil {
			data.UserProfile = strings.TrimRight(c, "\n")
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

	data.PromptSections = append(data.PromptSections, p.PromptSections...)

	// Project context.
	contextHost, closeContextHost := resolvePromptContextHost(ctx, p.Host, p.Cwd)
	defer closeContextHost()
	data.ContextFiles = loadProjectContextFiles(contextHost, p.Cwd)

	var buf bytes.Buffer
	_ = systemTmpl.Execute(&buf, data)
	return buf.String()
}

// loadProjectContextFiles walks from cwd up to the filesystem root,
// collecting AGENTS.md files from each directory (case-insensitive).
// Files are returned in root-to-leaf order (ancestors first).
func resolvePromptContextHost(ctx context.Context, host sandbox.Host, cwd string) (sandbox.Host, func()) {
	if host != nil || cwd == "" {
		return host, func() {}
	}

	session, err := sandbox.GlobalRegistry().CreateRelaxedSession(ctx, sandbox.Policy{
		Backend: "local",
		Filesystem: sandbox.FilesystemPolicy{
			WorkingDir:   cwd,
			AllowEscapes: true,
		},
		Network: sandbox.NetworkPolicy{Mode: sandbox.NetworkAllowAll},
	})
	if err != nil {
		return nil, func() {}
	}
	return session.Host(), func() { _ = session.Close() }
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
				if content, ok := readPromptFile(context.Background(), host, path); ok {
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
	if path, ok := statPromptFile(context.Background(), host, exact); ok {
		return path
	}
	entries, err := readPromptDir(context.Background(), host, dir)
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
