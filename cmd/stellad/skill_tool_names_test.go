package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/agent/toolmeta"
	"github.com/CherryHQ/stella/internal/connections"
	"github.com/CherryHQ/stella/internal/email"
	"github.com/CherryHQ/stella/internal/goal"
	"github.com/CherryHQ/stella/internal/recally"
	"github.com/CherryHQ/stella/internal/scheduler"
	sharepkg "github.com/CherryHQ/stella/internal/share"
	"github.com/CherryHQ/stella/internal/vault"
	workflowpkg "github.com/CherryHQ/stella/internal/workflow"
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
	// A union tool was referenced as "the `scheduler` tool". After the split
	// that names nothing callable, and the bare family name is too common a
	// word to flag on its own.
	unionMention = regexp.MustCompile("`(goal|scheduler|workflow|oauth|email|share|vault|recally)`\\s+tool")
)

// thirdPartySkills wrap another product's CLI, so their backticked identifiers
// are that product's field names (Lark's workflow_id), not Stella tools.
var thirdPartySkills = []string{"skills/system/lark-cli/"}

func toolMentions(text string) []string {
	var out []string
	for _, match := range backtickMention.FindAllStringSubmatch(text, -1) {
		out = append(out, match[1])
	}
	for _, match := range invokeMention.FindAllStringSubmatch(text, -1) {
		out = append(out, match[1])
	}
	for _, match := range unionMention.FindAllStringSubmatch(text, -1) {
		out = append(out, match[1])
	}
	return out
}

func TestBuiltinProseOnlyNamesRegisteredTools(t *testing.T) {
	registered := map[string]bool{}
	for _, family := range [][]toolmeta.ActionTool{
		goal.ActionTools(), scheduler.ActionTools(), workflowpkg.ActionTools(),
		connections.ActionTools(), email.ActionTools(), sharepkg.ActionTools(),
		vault.ActionTools(), recally.ActionTools(),
	} {
		for _, spec := range family {
			registered[spec.Name] = true
		}
	}

	for path, text := range builtinProse(t) {
		if isThirdPartySkill(path) {
			continue
		}
		for _, mention := range toolMentions(text) {
			if registered[mention] || toolmeta.HandWritten(mention) {
				continue
			}
			t.Errorf("%s names %q, which no family registers", path, mention)
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

func isThirdPartySkill(path string) bool {
	for _, prefix := range thirdPartySkills {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}
