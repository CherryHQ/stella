// Package renderrefs defines the sideband protocol for lifting references from
// tool output so the chat UI can render a rich card instead of a raw UUID.
//
// A producer writes one sentinel line per reference via [Emit]. The consumer (the
// tool-result ingest path) runs [Extract] over the combined tool output to lift
// the references out and strip the sentinel lines, so the text shown to the model
// and the user stays clean. References are derived data: nothing is stored that a
// GET on the entity could not re-derive, so there is no migration and stale
// previews never lie — the frontend always hydrates by id.
package renderrefs

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// marker prefixes every sentinel line. The version is in the marker (not just
// the payload) so a future incompatible format can be introduced without the
// old extractor mis-parsing it.
const marker = "::stella-ref/v1::"

// Preview is a best-effort snapshot used only as a loading placeholder. It is
// never trusted: the frontend hydrates the live entity by id, so a forged or
// stale preview cannot grant access or misrepresent state past first paint.
type Preview struct {
	Title  string `json:"title,omitempty"`
	Status string `json:"status,omitempty"`
}

// Reference is one renderable entity reference. Type and ID are the only load-
// bearing fields; everything else is a hint.
type Reference struct {
	V       int      `json:"v"`
	Type    string   `json:"type"`               // "task" | "goal" | "recally_article" | future
	ID      string   `json:"id"`                 // entity UUID
	Intent  string   `json:"intent,omitempty"`   // "created" (default emitted) | "referenced"
	AgentID string   `json:"agent_id,omitempty"` // owning agent, for deep links to agent-scoped pages (task/goal)
	Preview *Preview `json:"preview,omitempty"`
}

// Emit writes one sentinel line for ref to w (typically a tool's stderr). It is
// a no-op when ref carries no id/type, so callers can emit unconditionally
// without guarding on partial data.
func Emit(w io.Writer, ref Reference) error {
	if ref.ID == "" || ref.Type == "" {
		return nil
	}
	if ref.V == 0 {
		ref.V = 1
	}
	payload, err := json.Marshal(ref)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "%s%s\n", marker, payload)
	return err
}

// Extract pulls every sentinel reference out of text and returns the text with
// those lines removed. Lines are matched anchored at their start (after leading
// whitespace), so a sentinel survives stdout/stderr interleaving and tail
// truncation as long as the line itself is intact. Malformed sentinel lines are
// dropped silently rather than surfaced as garbage to the user.
func Extract(text string) (clean string, refs []Reference) {
	if !strings.Contains(text, marker) {
		return text, nil
	}
	lines := strings.Split(text, "\n")
	kept := lines[:0]
	for _, line := range lines {
		// Only treat the marker as a sentinel when nothing but whitespace
		// precedes it; this avoids eating a line that merely mentions it. Such a
		// line is protocol, never user content, so it is always dropped — even
		// when the payload is malformed or truncated (e.g. a sentinel clipped by
		// tail truncation), which must not leak to the user as garbage.
		if before, payload, found := strings.Cut(line, marker); found && strings.TrimSpace(before) == "" {
			var ref Reference
			if err := json.Unmarshal([]byte(payload), &ref); err == nil && ref.ID != "" && ref.Type != "" {
				refs = append(refs, ref)
			}
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n"), refs
}
