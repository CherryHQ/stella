package feishu

import (
	"fmt"
	"net/url"
	"os"
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
		// An "open" button on its own paragraph so feishucard renders it as a
		// link button below the line. Omitted when no public URL is buildable.
		if link := entityURL(ref); link != "" {
			fmt.Fprintf(&b, "\n{{button label=\"打开 Web UI\" type=\"primary\" url=%q}}\n\n", link)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// entityURL builds an absolute deep link to the entity's Web UI page, or "" when
// it cannot be built (no STELLA_BASE_URL, or a task/goal without an owning agent).
// The web page enforces its own access control, so the link is safe to surface.
func entityURL(ref renderrefs.Reference) string {
	base := strings.TrimRight(os.Getenv("STELLA_BASE_URL"), "/")
	if base == "" {
		return ""
	}
	switch ref.Type {
	case "task":
		if ref.AgentID == "" {
			return ""
		}
		return fmt.Sprintf("%s/agents/%s/tasks/%s", base, ref.AgentID, ref.ID)
	case "goal":
		if ref.AgentID == "" {
			return ""
		}
		return fmt.Sprintf("%s/agents/%s/tasks/goals/%s", base, ref.AgentID, ref.ID)
	case "recally_article":
		return fmt.Sprintf("%s/recally?article=%s", base, url.QueryEscape(ref.ID))
	default:
		return ""
	}
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
