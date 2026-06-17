package feishu

import (
	"fmt"
	"strings"

	"github.com/CherryHQ/stella/internal/renderrefs"
)

func dedupeReferences(refs []renderrefs.Reference) []renderrefs.Reference {
	if len(refs) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(refs))
	out := make([]renderrefs.Reference, 0, len(refs))
	for _, ref := range refs {
		key := ref.Type + "\x00" + ref.ID
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, ref)
	}
	return out
}

func appendReferenceSection(response string, refs []renderrefs.Reference, isGroup bool) string {
	refs = dedupeReferences(refs)
	if len(refs) == 0 {
		return response
	}

	var b strings.Builder
	b.WriteString(strings.TrimRight(response, "\n"))
	b.WriteString("\n\n---\n")
	b.WriteString("**References**\n")
	for _, ref := range refs {
		b.WriteString(referenceLine(ref, isGroup))
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

func referenceLine(ref renderrefs.Reference, isGroup bool) string {
	title := ref.ID
	status := ""
	if ref.Preview != nil {
		if strings.TrimSpace(ref.Preview.Title) != "" {
			title = strings.TrimSpace(ref.Preview.Title)
		}
		status = strings.TrimSpace(ref.Preview.Status)
	}
	label := referenceTypeLabel(ref.Type, isGroup)
	line := fmt.Sprintf("- %s **%s**", label, title)
	if status != "" {
		line += " · " + status
	}
	return line
}

func referenceTypeLabel(refType string, isGroup bool) string {
	switch refType {
	case "task":
		if isGroup {
			return "📋"
		}
		return "📋 Task"
	case "goal":
		if isGroup {
			return "🎯"
		}
		return "🎯 Goal"
	case "recally_article":
		if isGroup {
			return "📄"
		}
		return "📄 Article"
	default:
		if isGroup {
			return "🔗"
		}
		return "🔗 Reference"
	}
}
