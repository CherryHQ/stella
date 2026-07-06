package feishu

import (
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/CherryHQ/stella/pkg/renderrefs"
)

// maxRenderedRefs caps how many reference cards a single reply renders, so a
// response that touches many entities can't flood the chat or blow past Feishu's
// per-card element limits. The remainder is summarized as "+N more".
const maxRenderedRefs = 10

func dedupeReferences(refs []renderrefs.Reference) []renderrefs.Reference {
	if len(refs) == 0 {
		return nil
	}
	index := make(map[string]int, len(refs))
	out := make([]renderrefs.Reference, 0, len(refs))
	for _, ref := range refs {
		key := ref.Type + "\x00" + ref.ID
		if i, ok := index[key]; ok {
			out[i] = mergeReference(out[i], ref)
			continue
		}
		index[key] = len(out)
		out = append(out, ref)
	}
	return out
}

// mergeReference fills gaps in the kept reference a from a later duplicate b: a
// present AgentID (needed for the deep link), a present Preview, and a "created"
// intent are all more useful than their absence, regardless of arrival order.
func mergeReference(a, b renderrefs.Reference) renderrefs.Reference {
	if a.AgentID == "" {
		a.AgentID = b.AgentID
	}
	a.Preview = mergePreview(a.Preview, b.Preview)
	if a.Intent != "created" && b.Intent == "created" {
		a.Intent = "created"
	}
	return a
}

// mergePreview fills empty Title/Status fields of a from b, so a duplicate that
// carries only the status doesn't shadow an earlier one that carried the title.
func mergePreview(a, b *renderrefs.Preview) *renderrefs.Preview {
	if b == nil {
		return a
	}
	if a == nil {
		return b
	}
	if a.Title == "" {
		a.Title = b.Title
	}
	if a.Status == "" {
		a.Status = b.Status
	}
	return a
}

func appendReferenceSection(response string, refs []renderrefs.Reference, isGroup bool) string {
	refs = dedupeReferences(refs)
	if len(refs) == 0 {
		return response
	}

	shown := refs
	if len(shown) > maxRenderedRefs {
		shown = shown[:maxRenderedRefs]
	}

	var b strings.Builder
	b.WriteString(strings.TrimRight(response, "\n"))
	b.WriteString("\n\n---\n")
	b.WriteString("**References**\n")
	for _, ref := range shown {
		b.WriteString(referenceLine(ref, isGroup))
		b.WriteByte('\n')
		// An "open" button on its own paragraph so feishucard renders it as a
		// link button below the line. Omitted when no public URL is buildable.
		if link := entityURL(ref); link != "" {
			fmt.Fprintf(&b, "\n{{button label=\"打开 Web UI\" type=\"primary\" url=%q}}\n\n", link)
		}
	}
	if extra := len(refs) - len(shown); extra > 0 {
		fmt.Fprintf(&b, "_+%d more_\n", extra)
	}
	return strings.TrimRight(b.String(), "\n")
}

// entityURL builds an absolute deep link to the entity's Web UI page, or "" when
// it cannot be built (no STELLA_BASE_URL, or a task/goal without an owning agent).
// The web page enforces its own access control, so the link is safe to surface.
func entityURL(ref renderrefs.Reference) string {
	base := strings.TrimRight(os.Getenv("STELLA_BASE_URL"), "/")
	if base == "" || !validBaseURL(base) {
		return ""
	}
	switch ref.Type {
	// Tasks and goals are both goals now; the legacy "task" type shares the
	// goal detail route.
	case "task", "goal":
		if ref.AgentID == "" {
			return ""
		}
		return base + "/agents/" + url.PathEscape(ref.AgentID) + "/goals/" + url.PathEscape(ref.ID)
	case "recally_article":
		return base + "/recally?article=" + url.QueryEscape(ref.ID)
	default:
		return ""
	}
}

// validBaseURL rejects a misconfigured STELLA_BASE_URL whose scheme could turn a
// card button into a non-web target (javascript:, data:, …).
func validBaseURL(base string) bool {
	u, err := url.Parse(base)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
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
	line := fmt.Sprintf("- %s **%s**", label, sanitizeInline(title))
	if status != "" {
		line += " · " + sanitizeInline(status)
	}
	return line
}

// bracketSanitizer defuses markdown link/image syntax (spoofed clickable text)
// an attacker could smuggle through an entity title/status. Brace directives are
// handled separately by sanitizeInline because a single-pass replacer can't break
// odd-length runs.
var bracketSanitizer = strings.NewReplacer(
	"[", "(",
	"]", ")",
)

// sanitizeInline makes agent/user-controlled text safe to interpolate into the
// reference card's markdown: it collapses whitespace (a newline would break the
// list item) and neutralizes injectable markup. feishucard scans the whole card
// for {{button ...}} directives, so a crafted title could otherwise inject a
// phishing button seen by every group member.
func sanitizeInline(s string) string {
	s = bracketSanitizer.Replace(strings.Join(strings.Fields(s), " "))
	// Break every run of "{" so no "{{" directive opener survives. A single
	// non-overlapping pass leaves odd runs exploitable ("{{{" -> "{ {{"), so loop
	// until none remain; "{ {" never reintroduces an adjacent pair, so it converges.
	for strings.Contains(s, "{{") {
		s = strings.ReplaceAll(s, "{{", "{ {")
	}
	return s
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
