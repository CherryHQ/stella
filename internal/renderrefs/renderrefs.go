// Package renderrefs defines the sideband protocol that lets agent CLI commands
// announce a freshly created (or referenced) Stella entity — a task, goal, or
// recally article — so the chat UI can render a rich card instead of a raw UUID.
//
// The producer (a CLI command, gated by [Enabled]) writes one sentinel line per
// reference to its stderr via [Emit]. The consumer (the tool-result ingest path)
// runs [Extract] over the combined tool output to lift the references out and
// strip the sentinel lines, so the text shown to the model and the user stays
// clean. References are derived data: nothing is stored that a GET on the entity
// could not re-derive, so there is no migration and stale previews never lie —
// the frontend always hydrates by id.
package renderrefs

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// marker prefixes every sentinel line. The version is in the marker (not just
// the payload) so a future incompatible format can be introduced without the
// old extractor mis-parsing it.
const marker = "::stella-ref/v1::"

// envVar gates emission. The bash tool sets it so the CLI only emits sentinels
// when run by an agent, never when a human runs the same command in a terminal.
const envVar = "STELLA_RENDERABLE_REFS"

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
	Type    string   `json:"type"`             // "task" | "goal" | "recally_article" | future
	ID      string   `json:"id"`               // entity UUID
	Intent  string   `json:"intent,omitempty"` // "created" (default emitted) | "referenced"
	Preview *Preview `json:"preview,omitempty"`
}

// Enabled reports whether the current process should emit sentinels.
func Enabled() bool { return os.Getenv(envVar) == "1" }

// Emit writes one sentinel line for ref to w (typically a command's stderr).
// It is a no-op (returning nil) unless emission is [Enabled], so a human running
// the same CLI in a terminal never sees the marker — only agent runs, where the
// bash tool sets the env, do. It is also a no-op when ref carries no id/type, so
// callers can emit unconditionally without guarding on partial data.
func Emit(w io.Writer, ref Reference) error {
	if !Enabled() {
		return nil
	}
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
		// precedes it; this avoids eating a line that merely mentions it.
		if before, payload, found := strings.Cut(line, marker); found && strings.TrimSpace(before) == "" {
			var ref Reference
			if err := json.Unmarshal([]byte(payload), &ref); err == nil && ref.ID != "" && ref.Type != "" {
				refs = append(refs, ref)
				continue
			}
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n"), refs
}
