package sandbox

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
	pkgtools "github.com/CherryHQ/stella/pkg/tools"
)

const (
	editMatchPreviewLimit       = 5
	editMatchPreviewWidth       = 120
	editMatchPreviewBeforeBytes = 32
	editMatchPreviewAfterBytes  = editMatchPreviewWidth * utf8.UTFMax
)

func editDefinition() pkgtools.Definition {
	return pkgtools.Definition{
		Name:        "edit",
		Description: "Make a surgical edit to a file. The old_string must match exactly (including whitespace and indentation). Use this for targeted changes to existing files.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":        map[string]any{"type": "string", "description": "Relative paths are working/project files. Supported roots: $HOME, $STELLA_ASSETS_DIR, and $TMPDIR. Default work to $HOME; save final user deliverables in $STELLA_ASSETS_DIR when available."},
				"old_string":  map[string]any{"type": "string", "description": "The exact text to find and replace. Must match the file content exactly."},
				"new_string":  map[string]any{"type": "string", "description": "The replacement text."},
				"replace_all": map[string]any{"type": "boolean", "default": false, "description": "Replace every occurrence of old_string. Defaults to false; when false, old_string must be unique."},
			},
			"required": []string{"path", "old_string", "new_string"},
		},
	}
}

func newEditTool(host pkgsandbox.Session) pkgtools.Tool {
	return &hostEditTool{host: host}
}

type hostEditTool struct {
	host pkgsandbox.Session
}

func (t *hostEditTool) Definition() pkgtools.Definition { return editDefinition() }

func (t *hostEditTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	path := pkgtools.StringArg(args, "path")
	oldStr := pkgtools.StringArg(args, "old_string")
	newStr := pkgtools.StringArg(args, "new_string")
	if path == "" {
		return "", fmt.Errorf("edit: path is required")
	}
	if oldStr == "" {
		return "", fmt.Errorf("edit: old_string is required")
	}

	view, err := pkgsandbox.SelectFileView(ctx, t.host)
	if err != nil {
		return "", fmt.Errorf("edit %s: %w", path, err)
	}
	resolvedPath, err := resolveToolExpression(view.Policy.Env, view.WorkingDir, path)
	if err != nil {
		return "", fmt.Errorf("edit %s: %w", path, err)
	}

	raw, err := view.Files.ReadFile(resolvedPath)
	if err != nil {
		return "", fmt.Errorf("edit: read %s: %w", path, err)
	}
	fileContent := string(raw)
	count, matchOffsets := editMatchOffsets(fileContent, oldStr)
	if count == 0 {
		return "", fmt.Errorf("edit: old_string not found in %s", path)
	}

	replaceAll, _ := args["replace_all"].(bool)
	if count > 1 && !replaceAll {
		return "", fmt.Errorf("%s", editUniquenessError(path, fileContent, count, matchOffsets))
	}

	replacements := 1
	if replaceAll {
		replacements = count
	}
	updated := strings.Replace(fileContent, oldStr, newStr, replacements)
	if err := view.Files.WriteFile(resolvedPath, []byte(updated), 0o644); err != nil {
		return "", fmt.Errorf("edit %s: %w", path, err)
	}
	if replaceAll {
		occurrence := "occurrence"
		if count != 1 {
			occurrence += "s"
		}
		return fmt.Sprintf("Edited %s: replaced %d %s", path, count, occurrence), nil
	}
	return fmt.Sprintf("Edited %s", path), nil
}

func editMatchOffsets(content, old string) (int, []int) {
	count := 0
	offsets := make([]int, 0, editMatchPreviewLimit)
	for searchFrom := 0; searchFrom <= len(content)-len(old); {
		relative := strings.Index(content[searchFrom:], old)
		if relative < 0 {
			break
		}
		offset := searchFrom + relative
		count++
		if len(offsets) < editMatchPreviewLimit {
			offsets = append(offsets, offset)
		}
		searchFrom = offset + len(old)
	}
	return count, offsets
}

func editUniquenessError(path, content string, count int, offsets []int) string {
	var message strings.Builder
	fmt.Fprintf(&message, "edit: old_string matches %d times in %s (must be unique). Showing first %d of %d matches:", count, path, len(offsets), count)
	for i, offset := range offsets {
		line, column, preview := editMatchPreview(content, offset)
		fmt.Fprintf(&message, "\n%d. line %d, column %d: %s", i+1, line, column, preview)
	}
	fmt.Fprintf(&message, "\nUse a more specific old_string, or set replace_all: true to replace all %d matches.", count)
	return message.String()
}

func editMatchPreview(content string, offset int) (int, int, string) {
	line := strings.Count(content[:offset], "\n") + 1
	lineStart := strings.LastIndex(content[:offset], "\n") + 1
	column := utf8.RuneCountInString(content[lineStart:offset]) + 1
	lineEnd := len(content)
	if relative := strings.IndexByte(content[offset:], '\n'); relative >= 0 {
		lineEnd = offset + relative
	}

	windowStart := max(lineStart, offset-editMatchPreviewBeforeBytes)
	for windowStart < offset && !utf8.RuneStart(content[windowStart]) {
		windowStart++
	}
	windowEnd := min(lineEnd, offset+editMatchPreviewAfterBytes)
	for windowEnd > offset && windowEnd < len(content) && !utf8.RuneStart(content[windowEnd]) {
		windowEnd--
	}

	preview := strings.TrimSuffix(content[windowStart:windowEnd], "\r")
	preview = strings.ReplaceAll(preview, "\t", `\t`)
	preview = strings.ReplaceAll(preview, "\r", `\r`)
	runes := []rune(preview)
	prefixOmitted := windowStart > lineStart
	suffixOmitted := windowEnd < lineEnd
	available := editMatchPreviewWidth
	if prefixOmitted {
		available--
	}
	if suffixOmitted {
		available--
	}
	if len(runes) > available && !suffixOmitted {
		suffixOmitted = true
		available--
	}
	if len(runes) > available {
		runes = runes[:available]
	}

	var bounded strings.Builder
	if prefixOmitted {
		bounded.WriteRune('…')
	}
	for _, r := range runes {
		bounded.WriteRune(r)
	}
	if suffixOmitted {
		bounded.WriteRune('…')
	}
	return line, column, bounded.String()
}
