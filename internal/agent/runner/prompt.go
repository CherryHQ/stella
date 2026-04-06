package runner

import (
	"bytes"
	"context"
	_ "embed"
	"os"
	"path/filepath"
	"strings"

	"github.com/vaayne/anna/pkg/memory"
)

//go:embed template/system.md
var defaultBasicPrompt string

// contextFile represents a discovered AGENTS.md file with its path and content.
type contextFile struct {
	Path    string
	Content string
}

// DBPromptParams holds the parameters for building a system prompt from DB-backed config.
type DBPromptParams struct {
	SystemPrompt  string          // agent's base identity from agents.system_prompt (immutable)
	Memory        memory.Provider // active provider for profile loading (may be nil)
	UserID        int64           // auth user ID for profile lookup
	AgentID       string          // agent ID for profile lookup
	AnnaHome      string
	Workspace     string
	Cwd           string // optional working directory
	UserSkillsDir string // optional per-user skills directory
}

// BuildSystemPromptFromDB composes the full system prompt in layers:
//
//  1. Base system prompt — embedded default, overridden by SYSTEM.md in workspace
//  2. Agent base identity — from DB agents.system_prompt (immutable, shared across users)
//  3. Agent soul — per-user identity/personality customisation from memory ProfileStore
//  4. User profile — per-user facts/context from memory ProfileStore
//
// Skills and project context are appended after these layers.
func BuildSystemPromptFromDB(ctx context.Context, p DBPromptParams) string {
	// Layer 1: Base system prompt.
	// SYSTEM.md in workspace overrides the embedded default.
	basic := defaultBasicPrompt
	if content := readFileIfExists(p.Workspace, "SYSTEM.md"); content != "" {
		basic = content
	}

	var buf bytes.Buffer
	buf.WriteString(strings.TrimRight(basic, "\n"))

	// Layer 2: Agent base identity (immutable, from DB).
	if p.SystemPrompt != "" {
		buf.WriteString("\n\n## Identity\n\n")
		buf.WriteString(strings.TrimRight(p.SystemPrompt, "\n"))
	}

	// Layers 3 & 4: Agent soul + user profile from memory ProfileStore.
	// Always present so the agent knows it can populate them via the memory tool.
	if ps, ok := p.Memory.(memory.ProfileStore); ok && p.UserID > 0 && p.AgentID != "" {
		var soul string
		if s, err := ps.GetAgentSoul(ctx, p.UserID, p.AgentID); err == nil {
			soul = strings.TrimRight(s, "\n")
		}
		writeProfileSection(&buf, "Agent Soul",
			"Your identity, personality, and behavior. Edit via the memory tool (soul_get / soul_update).",
			"agent_soul", soul)

		var profile string
		if c, err := ps.GetProfile(ctx, p.UserID, p.AgentID); err == nil {
			profile = strings.TrimRight(c, "\n")
		}
		writeProfileSection(&buf, "User Profile",
			"What you know about this user across conversations. Edit via the memory tool (profile_get / profile_update).",
			"user_profile", profile)
	}

	// Skills.
	if skills := FormatSkillsForPrompt(LoadSkills(p.AnnaHome, p.Workspace, p.Cwd, p.UserSkillsDir)); skills != "" {
		buf.WriteString("\n")
		buf.WriteString(skills)
	}

	// Project context (AGENTS.md files).
	if ctxFiles := loadProjectContextFiles(p.Cwd); len(ctxFiles) > 0 {
		buf.WriteString("\n\n# Project Context\n\n")
		buf.WriteString("Project-specific instructions and guidelines:\n\n")
		for _, f := range ctxFiles {
			buf.WriteString("## " + f.Path + "\n\n")
			buf.WriteString(strings.TrimRight(f.Content, "\n"))
			buf.WriteString("\n\n")
		}
	}

	return buf.String()
}

// writeProfileSection appends a titled, XML-tagged memory section to buf.
func writeProfileSection(buf *bytes.Buffer, heading, description, xmlTag, content string) {
	buf.WriteString("\n\n## ")
	buf.WriteString(heading)
	buf.WriteString("\n\n")
	buf.WriteString(description)
	buf.WriteString("\n\n<")
	buf.WriteString(xmlTag)
	buf.WriteString(">\n")
	buf.WriteString(content)
	buf.WriteString("\n</")
	buf.WriteString(xmlTag)
	buf.WriteString(">")
}

// readFileIfExists reads a file from dir with case-insensitive matching.
// Returns the trimmed content, or empty string if not found.
func readFileIfExists(dir, name string) string {
	path := resolveFile(dir, name)
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// loadProjectContextFiles walks from cwd up to the filesystem root,
// collecting AGENTS.md files from each directory (case-insensitive).
// Files are returned in root-to-leaf order (ancestors first).
func loadProjectContextFiles(cwd string) []contextFile {
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
		if path := resolveFile(absDir, "AGENTS.md"); path != "" {
			if !seen[path] {
				seen[path] = true
				if data, err := os.ReadFile(path); err == nil {
					files = append(files, contextFile{Path: path, Content: string(data)})
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
func resolveFile(dir, name string) string {
	exact := filepath.Join(dir, name)
	if _, err := os.Stat(exact); err == nil {
		return exact
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	target := strings.ToLower(name)
	for _, e := range entries {
		if strings.ToLower(e.Name()) == target {
			return filepath.Join(dir, e.Name())
		}
	}
	return ""
}
