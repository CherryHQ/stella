package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"

	"github.com/CherryHQ/stella/internal/agent/toolmeta"
	"github.com/CherryHQ/stella/resources"
)

// Skills and the system prompt name tools as prose, and prose does not compile.
// A union tool absorbed a wrong name silently — "action=pause" on the right
// family still reached a real tool — but a split family has one name per
// action, so a stale mention is a call the model cannot make.
//
// This is the generalization of the recally-only guard in
// internal/scheduler/builtin_schema_test.go: every mention of a generated
// family's prefix, anywhere in the built-in skills or the prompt template, must
// be a tool this build registers.
//
// cmd/stellad is the only package that already imports every family, which is
// why the guard lives beside the registration rather than beside the skills.
// A mention is a whole backtick span or a Code Mode invocation, which is how a
// skill writes a tool name. Matching bare tokens instead would flag every field
// called workflow_id, and a guard with false positives gets deleted.
var (
	backtickMention = regexp.MustCompile("`((?:goal|scheduler|workflow|oauth|email|share|vault|recally)_[a-z_]+)`")
	invokeMention   = regexp.MustCompile(`tools\.invoke\(\s*"([a-z_]+)"`)
	// A union tool was referenced as "the `scheduler` tool", or called as
	// "`oauth connect(provider=feishu)`" — the union's own argument syntax.
	// After the split neither names anything callable, and the bare family word
	// is too common to flag on its own.
	unionMention     = regexp.MustCompile("`(goal|scheduler|workflow|oauth|email|share|vault|recally)`\\s+tool")
	unionCallMention = regexp.MustCompile("`((?:goal|scheduler|workflow|oauth|email|share|vault|recally) +[a-z_]+)[^`]*`")
)

// thirdPartyFields are identifiers that read like a Stella tool name but belong
// to another product's API — Lark's `workflow_id`, its `share_*` chat fields.
// They are listed one by one rather than skipped by path: a directory-wide skip
// is what let five retired `oauth` instructions survive the split inside
// lark-cli. Adding an entry is a claim that the token is a field of a
// third-party API, and it is the only way a backticked `family_*` token escapes
// being checked against the registry.
var thirdPartyFields = map[string]bool{
	"workflow_id": true, // Lark workflow/approval instance id
	"share_chat":  true, // Lark message type
	"share_info":  true, // Lark share payload
	"share_link":  true, // Lark share payload
	"share_user":  true, // Lark message type
}

// toolMentions returns names the prose asks the model to call. They must all be
// tools this build registers.
func toolMentions(text string) []string {
	var out []string
	for _, match := range backtickMention.FindAllStringSubmatch(text, -1) {
		if thirdPartyFields[match[1]] {
			continue
		}
		out = append(out, match[1])
	}
	for _, match := range invokeMention.FindAllStringSubmatch(text, -1) {
		out = append(out, match[1])
	}
	return out
}

// proseProblems is what the guard reports for one document. It is separate from
// the walk so the guard's own tests can drive it against the real registry.
func proseProblems(text string, registered map[string]bool) []string {
	var out []string
	for _, mention := range toolMentions(text) {
		if registered[mention] || toolmeta.HandWritten(mention) {
			continue
		}
		out = append(out, fmt.Sprintf("names %q, which no family registers", mention))
	}
	for _, mention := range unionCallMentions(text) {
		out = append(out, fmt.Sprintf("says %q, which addresses a family that is no longer one tool", mention))
	}
	return out
}

// registeredToolNames is every generated tool name this build registers.
func registeredToolNames() map[string]bool {
	registered := map[string]bool{}
	for _, family := range generatedFamilies() {
		for _, spec := range family {
			registered[spec.Name] = true
		}
	}
	return registered
}

// unionCallMentions returns prose that still addresses a family the way the
// union was addressed. Every one of these is wrong regardless of what this
// build registers, because no union tool exists any more.
func unionCallMentions(text string) []string {
	var out []string
	for _, match := range unionMention.FindAllStringSubmatch(text, -1) {
		out = append(out, match[1]+" tool")
	}
	for _, match := range unionCallMention.FindAllStringSubmatch(text, -1) {
		out = append(out, match[1])
	}
	return out
}

func TestBuiltinProseOnlyNamesRegisteredTools(t *testing.T) {
	registered := registeredToolNames()
	for path, text := range builtinProse(t) {
		for _, problem := range proseProblems(text, registered) {
			t.Errorf("%s %s", path, problem)
		}
	}
}

// builtinProse is every embedded skill document plus the system prompt
// template: the two surfaces that tell a model what to call.
func builtinProse(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	skills := resources.FS()
	err := fs.WalkDir(skills, "skills", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		if ext := filepath.Ext(path); ext != ".md" && ext != ".mdx" {
			return nil
		}
		body, readErr := fs.ReadFile(skills, path)
		if readErr != nil {
			return readErr
		}
		out[path] = string(body)
		return nil
	})
	if err != nil {
		t.Fatalf("walk builtin skills: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("no builtin skills found: the walk root is wrong, not the skills")
	}
	// The prompt templates are embedded into internal/agent/prompt behind
	// unexported variables, so they are read from the tree instead. A test
	// binary always runs in its own package directory, which makes the relative
	// path stable.
	templates, err := filepath.Glob(filepath.Join("..", "..", "internal", "agent", "prompt", "template", "*"))
	if err != nil {
		t.Fatalf("glob prompt templates: %v", err)
	}
	if len(templates) == 0 {
		t.Fatal("no prompt templates found: the relative path is wrong, not the templates")
	}
	for _, path := range templates {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		out[path] = string(body)
	}
	return out
}

// The guard is regex prose matching, so it needs its own guard: a pattern that
// silently stops matching turns the whole test green for the wrong reason.
func TestProseGuardDetectsRetiredCallSyntax(t *testing.T) {
	for _, tc := range []struct {
		name      string
		text      string
		wantNames []string
		wantUnion []string
	}{
		{
			name:      "backticked tool name is a mention",
			text:      "call `recally_article_save` with the fetched body",
			wantNames: []string{"recally_article_save"},
		},
		{
			name:      "code mode invocation is a mention",
			text:      `return await tools.invoke("share_create_article", {})`,
			wantNames: []string{"share_create_article"},
		},
		{
			name:      "the union addressed as a tool",
			text:      "use the `oauth` tool for authorization",
			wantUnion: []string{"oauth tool"},
		},
		{
			name:      "the union called with its own action argument",
			text:      "run `oauth status`, then `oauth connect(provider=feishu)`",
			wantUnion: []string{"oauth status", "oauth connect"},
		},
		{
			// The allowlist covers the exact field, not the document it appears
			// in: a retired union call in the same sentence is still reported.
			name:      "an allowlisted third-party field does not shield its neighbours",
			text:      "pass `workflow_id` and then use `oauth list`",
			wantUnion: []string{"oauth list"},
		},
		{
			name: "allowlisted third-party fields are not mentions",
			text: "pass `workflow_id`, `share_chat`, `share_info`, `share_link` and `share_user`",
		},
		{
			name: "a bare family word in prose is not a mention",
			text: "the scheduler runs jobs; share the article by email",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := toolMentions(tc.text); !slices.Equal(got, tc.wantNames) {
				t.Errorf("toolMentions = %v, want %v", got, tc.wantNames)
			}
			if got := unionCallMentions(tc.text); !slices.Equal(got, tc.wantUnion) {
				t.Errorf("unionCallMentions = %v, want %v", got, tc.wantUnion)
			}
		})
	}
}

// A guard that only ever runs against correct prose proves nothing. These are
// the documents the guard exists to reject, checked against the real registry:
// if any of them comes back clean, the guard is decorative.
func TestProseGuardRejectsStaleToolNames(t *testing.T) {
	registered := registeredToolNames()
	for _, tc := range []struct {
		name string
		text string
		want string
	}{
		{
			name: "a tool name with a stale suffix",
			text: "call `oauth_connect_old` to authorize",
			want: `names "oauth_connect_old", which no family registers`,
		},
		{
			name: "a pre-split recally name",
			text: "call `recally_save_article` with the fetched body",
			want: `names "recally_save_article", which no family registers`,
		},
		{
			name: "a Code Mode invocation of a retired union",
			text: `await tools.invoke("scheduler", {action: "pause"})`,
			want: `names "scheduler", which no family registers`,
		},
		{
			name: "a third-party skill's own field name is not a licence for a stale tool",
			text: "pass `workflow_id`, then call `workflow_execute`",
			want: `names "workflow_execute", which no family registers`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			problems := proseProblems(tc.text, registered)
			if !slices.Contains(problems, tc.want) {
				t.Fatalf("problems = %v, want one of them to be %q", problems, tc.want)
			}
		})
	}

	// The counterpart: prose that names only real tools must come back clean,
	// or the guard is noise everyone learns to ignore.
	clean := "call `oauth_connect`, then `oauth_flow_status`; pass `workflow_id` and `share_chat` to lark-cli"
	if problems := proseProblems(clean, registered); len(problems) != 0 {
		t.Fatalf("correct prose reported %v", problems)
	}
}
