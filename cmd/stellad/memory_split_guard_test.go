package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/memory"
)

// The memory union kept fourteen actions the split does not carry forward, and
// it configured itself through options the split has no use for. Deleting them
// once is not enough: the option constructor is the shape a later family split
// is most likely to copy back in, and an action name reintroduced as a literal
// is a tool call no model can make.
//
// The scan is over source text rather than over the type system on purpose. A
// stale name usually comes back in a comment, a prompt string, or a test
// fixture, none of which the compiler sees.
var (
	// The thirteen deleted action names that carry an underscore. They are
	// specific enough to match as a bare quoted literal.
	deletedMemoryActionLiterals = []string{
		"get_message", "search_knowledge",
		"soul_get", "soul_update",
		"profile_get", "profile_update", "profile_history", "profile_rollback",
		"constraint_list", "constraint_add", "constraint_remove",
		"describe", "expand",
	}
	// `status` is the fourteenth, and `search`/`read` survive only as tool-name
	// suffixes. All three are ordinary English, so they are guarded through the
	// constant identifiers the union dispatched on instead: `actionStatus` can
	// only come back with the union.
	deletedMemoryIdentifiers = []string{
		"actionStatus", "actionSearch", "actionRead", "actionDescribe", "actionExpand",
		"actionGetMessage", "actionSoulGet", "actionSoulUpdate",
		"actionProfileGet", "actionProfileUpdate", "actionProfileHistory", "actionProfileRollback",
		"actionSearchKnowledge",
		"actionConstraintList", "actionConstraintAdd", "actionConstraintRemove",
		// The union's construction API. `BuildTool` returned one tool holding
		// every action; `ToolOption` and `WithActionsOnly` trimmed that set per
		// call site. A split family declares its actions in the spec instead.
		"ToolOption", "BuildTool", "WithActionsOnly",
	}
	deletedMemoryActionCount = 14
)

func TestRetiredMemoryUnionNamesAreGoneForGood(t *testing.T) {
	if got := len(deletedMemoryActionLiterals) + 1; got != deletedMemoryActionCount {
		t.Fatalf("guard covers %d deleted actions, want %d", got, deletedMemoryActionCount)
	}

	patterns := make(map[string]*regexp.Regexp, len(deletedMemoryActionLiterals)+len(deletedMemoryIdentifiers))
	for _, name := range deletedMemoryActionLiterals {
		patterns[`"`+name+`"`] = regexp.MustCompile(`"` + regexp.QuoteMeta(name) + `"`)
	}
	for _, name := range deletedMemoryIdentifiers {
		patterns[name] = regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\b`)
	}

	for _, dir := range []string{filepath.Join("..", "..", "internal", "memory"), "."} {
		err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".go") {
				return err
			}
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			// This file names every one of them, which is the point of it.
			if filepath.Base(path) == "memory_split_guard_test.go" {
				return nil
			}
			for label, pattern := range patterns {
				if loc := pattern.FindIndex(body); loc != nil {
					line := 1 + strings.Count(string(body[:loc[0]]), "\n")
					t.Errorf("%s:%d mentions %s, retired with the memory union", path, line, label)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}
}

// The memory tools take the standard fail-closed visibility check, like every
// other split family. The union they replaced was registered with no check at
// all, and the first draft of this split kept that; it was wrong twice over. It
// was not needed for group turns — a group session is owned by the group, so
// its RunnerParams.UserID is the GroupID and the standard check passes there —
// and skipping the gate offered both tools to a runner with no identity, whose
// every call then failed closed one layer down.
//
// The assertion runs against newBuiltinTools, the production path, so a future
// registration that quietly drops the check fails here rather than in review.
func TestMemoryToolsTakeTheStandardVisibilityCheck(t *testing.T) {
	names := map[string]bool{}
	for _, spec := range memory.ActionTools() {
		names[spec.Name] = true
	}

	seen := 0
	for _, builtin := range newBuiltinTools(builtinToolDeps{}) {
		if builtin.Tool == nil || !names[builtin.Tool.Definition().Name] {
			continue
		}
		seen++
		name := builtin.Tool.Definition().Name
		if builtin.Available == nil {
			t.Errorf("%s registers with no visibility check", name)
			continue
		}
		// Identity, not configuration: a runner missing either half of it is
		// not a caller these tools can answer, and the refusal belongs at the
		// gate rather than inside every handler.
		for _, tc := range []struct {
			what   string
			params agent.RunnerParams
			want   bool
		}{
			{"a user run", agent.RunnerParams{UserID: "user-1", AgentID: "agent-1"}, true},
			{"a group run, whose UserID is its GroupID", agent.RunnerParams{UserID: "group-1", AgentID: "agent-1", GroupID: "group-1"}, true},
			{"a runner with no user", agent.RunnerParams{AgentID: "agent-1"}, false},
			{"a runner with no agent", agent.RunnerParams{UserID: "user-1"}, false},
		} {
			got, err := builtin.Available(t.Context(), tc.params)
			if err != nil {
				t.Errorf("%s availability for %s: %v", name, tc.what, err)
				continue
			}
			if got != tc.want {
				t.Errorf("%s available for %s = %v, want %v", name, tc.what, got, tc.want)
			}
		}
	}
	if seen != len(memory.ActionTools()) {
		t.Fatalf("newBuiltinTools registered %d memory tools, want %d", seen, len(memory.ActionTools()))
	}
}
