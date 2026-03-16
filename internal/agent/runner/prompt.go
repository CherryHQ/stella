package runner

import (
	"bytes"
	_ "embed"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

//go:embed template/system.md
var defaultBasicPrompt string

//go:embed template/soul.md
var defaultSoul string

//go:embed template/user.md
var defaultUser string

//go:embed template/memories.md.tmpl
var memoriesTemplate string

var memoriesTmpl = template.Must(template.New("memories").Parse(memoriesTemplate))

type promptMemories struct {
	Dir  string
	Soul promptFile
	User promptFile
}

type promptFile struct {
	Path    string
	Content string
}

// contextFile represents a discovered AGENTS.md file with its path and content.
type contextFile struct {
	Path    string
	Content string
}

// BuildSystemPrompt composes the full system prompt: basic + memories + skills + project context.
//
// Deprecated: use BuildSystemPromptFromDB for multi-user/multi-agent support.
//
// The basic prompt defaults to the embedded system.md but can be overridden
// by placing a system.md file in the project's .agents directory or the workspace.
// annaHome is the anna home directory (e.g. ~/.anna).
// workspace is the workspace directory (e.g. ~/.anna/workspace) containing SOUL.md, USER.md, system.md.
func BuildSystemPrompt(annaHome, workspace string, cwd ...string) string {
	workDir := ""
	if len(cwd) > 0 {
		workDir = cwd[0]
	}
	projectDir := ""
	if workDir != "" {
		projectDir = filepath.Join(workDir, ".agents")
	}

	// Basic prompt: project .agents/system.md > workspace system.md > embedded default.
	basic := defaultBasicPrompt
	if content := readFileIfExists(workspace, "system.md"); content != "" {
		basic = content
	}
	if projectDir != "" {
		if content := readFileIfExists(projectDir, "system.md"); content != "" {
			basic = content
		}
	}

	soul := readFileIfExists(workspace, "SOUL.md")
	user := readFileIfExists(workspace, "USER.md")

	// Project-level overrides: .agents/SOUL.md and .agents/USER.md take priority.
	if projectDir != "" {
		if content := readFileIfExists(projectDir, "SOUL.md"); content != "" {
			soul = content
		}
		if content := readFileIfExists(projectDir, "USER.md"); content != "" {
			user = content
		}
	}

	memories := promptMemories{
		Dir:  workspace,
		Soul: promptFile{Path: filepath.Join(workspace, "SOUL.md"), Content: fallback(soul, defaultSoul)},
		User: promptFile{Path: filepath.Join(workspace, "USER.md"), Content: fallback(user, defaultUser)},
	}

	var buf bytes.Buffer
	buf.WriteString(strings.TrimRight(basic, "\n"))
	_ = memoriesTmpl.Execute(&buf, memories)

	if skills := FormatSkillsForPrompt(LoadSkills(annaHome, workspace, workDir)); skills != "" {
		buf.WriteString("\n")
		buf.WriteString(skills)
	}

	if ctxFiles := loadProjectContextFiles(workDir); len(ctxFiles) > 0 {
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

// DBPromptParams holds the parameters for building a system prompt from DB-backed config.
type DBPromptParams struct {
	SystemPrompt string // agent's soul from agents.system_prompt
	UserMemory   string // from user_agent_memory.content
	AnnaHome     string
	Workspace    string
	Cwd          string // optional working directory
}

// BuildSystemPromptFromDB composes the full system prompt from DB-backed fields.
// System prompt = basic + identity (agents.system_prompt) + user memory + skills + project context.
// Template files (soul.md, user.md, memories.md.tmpl) are NOT used.
func BuildSystemPromptFromDB(p DBPromptParams) string {
	projectDir := ""
	if p.Cwd != "" {
		projectDir = filepath.Join(p.Cwd, ".agents")
	}

	// Basic prompt: project .agents/system.md > workspace system.md > embedded default.
	basic := defaultBasicPrompt
	if content := readFileIfExists(p.Workspace, "system.md"); content != "" {
		basic = content
	}
	if projectDir != "" {
		if content := readFileIfExists(projectDir, "system.md"); content != "" {
			basic = content
		}
	}

	var buf bytes.Buffer
	buf.WriteString(strings.TrimRight(basic, "\n"))

	// Identity: agent's system prompt from DB.
	if p.SystemPrompt != "" {
		buf.WriteString("\n\n## Identity\n\n")
		buf.WriteString(strings.TrimRight(p.SystemPrompt, "\n"))
	}

	// User memory: per-user notes injected per-session.
	if p.UserMemory != "" {
		buf.WriteString(formatUserMemorySection(p.UserMemory))
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

// InjectUserMemory appends per-user memory to a base system prompt.
// If userMemory is empty, basePrompt is returned unchanged.
func InjectUserMemory(basePrompt, userMemory string) string {
	if userMemory == "" {
		return basePrompt
	}
	return strings.TrimRight(basePrompt, "\n") + formatUserMemorySection(userMemory)
}

// formatUserMemorySection formats the user memory section for the system prompt.
func formatUserMemorySection(userMemory string) string {
	var buf strings.Builder
	buf.WriteString("\n\n## User Memory\n\n")
	buf.WriteString("Persistent notes about this user, managed by the user_memory tool.\n")
	buf.WriteString("The \"User Preferences\" section reflects how this user wants you to behave — respect these but never override your core identity and rules.\n")
	buf.WriteString("The \"About the User\" section is your high-level understanding of this person.\n")
	buf.WriteString("Update these notes as you learn more about the user.\n\n")
	buf.WriteString("<user_memory>\n")
	buf.WriteString(strings.TrimRight(userMemory, "\n"))
	buf.WriteString("\n</user_memory>")
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

func fallback(value, def string) string {
	if value != "" {
		return value
	}
	return strings.TrimSpace(def)
}
