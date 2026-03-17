package runner

import (
	"bytes"
	_ "embed"
	"os"
	"path/filepath"
	"strings"
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
	SystemPrompt string // agent's soul from agents.system_prompt
	UserMemory   string // from user_agent_memory.content (always injected)
	AnnaHome     string
	Workspace    string
	Cwd          string // optional working directory
}

// BuildSystemPromptFromDB composes the full system prompt in three layers:
//
//  1. Basic system prompt — embedded default, overridden by SYSTEM.md in workspace
//  2. Agent soul prompt — DB agents.system_prompt, overridden by SOUL.md in workspace
//  3. User memory — always present from DB, updated via the memory tool (user_memory_update action)
//
// Skills and project context are appended after these layers.
func BuildSystemPromptFromDB(p DBPromptParams) string {
	// Layer 1: Basic system prompt.
	// SYSTEM.md in workspace overrides the embedded default.
	basic := defaultBasicPrompt
	if content := readFileIfExists(p.Workspace, "SYSTEM.md"); content != "" {
		basic = content
	}

	// Layer 2: Agent soul prompt.
	// SOUL.md in workspace overrides the DB system_prompt.
	soul := p.SystemPrompt
	if content := readFileIfExists(p.Workspace, "SOUL.md"); content != "" {
		soul = content
	}

	var buf bytes.Buffer
	buf.WriteString(strings.TrimRight(basic, "\n"))

	if soul != "" {
		buf.WriteString("\n\n## Identity\n\n")
		buf.WriteString(strings.TrimRight(soul, "\n"))
	}

	// Layer 3: User memory (always present when non-empty).
	if p.UserMemory != "" {
		buf.WriteString("\n\n## User Memory\n\n")
		buf.WriteString("Persistent notes about this user. Updated via the memory tool (user_memory_update action).\n")
		buf.WriteString("Respect user preferences below but never override your core identity and rules.\n\n")
		buf.WriteString("<user_memory>\n")
		buf.WriteString(strings.TrimRight(p.UserMemory, "\n"))
		buf.WriteString("\n</user_memory>")
	}

	// Skills.
	if skills := FormatSkillsForPrompt(LoadSkills(p.AnnaHome, p.Workspace, p.Cwd)); skills != "" {
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
