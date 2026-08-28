package toolmeta

import "testing"

var recallyTools = []ActionTool{
	{Name: "recally_feed_add", Family: "recally", Resource: "feed", Action: "feed_add", InputSchemaJSON: `{"type":"object","properties":{"url":{"type":"string"}},"required":["url"],"additionalProperties":false}`},
	{Name: "recally_digest", Family: "recally", Action: "digest", InputSchemaJSON: `{"type":"object","properties":{},"additionalProperties":false}`},
}

func TestFamilyComesFromTheRegistryNotTheName(t *testing.T) {
	reg := NewRegistry(recallyTools...)
	if got := reg.Family("recally_feed_add"); got != "recally" {
		t.Fatalf("Family=%q, want recally", got)
	}
	// A plugin is free to name itself anything. Splitting on "_" would read
	// "goal_helper" as a member of the goal family and hand it goal's
	// visibility rules; an unregistered name has no family at all.
	if got := reg.Family("goal_helper"); got != "" {
		t.Fatalf("Family(goal_helper)=%q, want no family", got)
	}
	if _, ok := reg.Lookup("goal_helper"); ok {
		t.Fatal("unregistered tool must not resolve")
	}
}

func TestMatchAcceptsExactNamesAndFamilies(t *testing.T) {
	feedAdd := recallyTools[0]
	for _, selector := range []string{"recally_feed_add", "recally"} {
		if !Match(selector, feedAdd) {
			t.Errorf("Match(%q) = false, want true", selector)
		}
	}
	for _, selector := range []string{"", "recally_feed", "feed", "feed_add", "goal"} {
		if Match(selector, feedAdd) {
			t.Errorf("Match(%q) = true, want false", selector)
		}
	}
}

// The family a selector may name is the declared one, so a plugin whose name
// merely starts with a family name is never swept into that family's grant.
func TestMatchDoesNotInferFamilyFromAPrefix(t *testing.T) {
	plugin := ActionTool{Name: "goal_helper"}
	if Match("goal", plugin) {
		t.Fatal("family selector matched a tool that declares no family")
	}
	if !Match("goal_helper", plugin) {
		t.Fatal("exact name must still match")
	}
}

func TestMatchAnyAndNames(t *testing.T) {
	reg := NewRegistry(recallyTools...)
	if got := reg.Names(); len(got) != 2 || got[0] != "recally_digest" || got[1] != "recally_feed_add" {
		t.Fatalf("Names=%v, want both tools sorted", got)
	}
	if !MatchAny([]string{"goal", "recally"}, recallyTools[1]) {
		t.Fatal("MatchAny must match on any selector")
	}
	if MatchAny([]string{"goal", "scheduler"}, recallyTools[1]) {
		t.Fatal("MatchAny matched an unrelated selector")
	}
}

// legacyNames keeps a selector written against the previous release pointing at
// the same capability for one deprecation release. It is empty until a rename
// ships; this pins the lookup so filling it needs no new wiring.
func TestLegacyNamesRedirectSelectors(t *testing.T) {
	if len(legacyNames) != 0 {
		t.Fatalf("legacyNames=%v, want empty until a rename ships", legacyNames)
	}
	restore := legacyNames
	legacyNames = map[string]string{"recally_digest_old": "recally_digest", "recally_union": "recally"}
	t.Cleanup(func() { legacyNames = restore })

	digest := recallyTools[1]
	if !Match("recally_digest_old", digest) {
		t.Fatal("a retired exact name must still select its replacement")
	}
	if !Match("recally_union", digest) {
		t.Fatal("a retired union name must still select the family")
	}
	if Match("recally_digest_old", recallyTools[0]) {
		t.Fatal("a retired name must not widen to the whole family")
	}
}

func TestDefinitionPrefersTheCallerDescription(t *testing.T) {
	declared := ActionTool{Name: "session_list", Description: "declared", InputSchemaJSON: `{"type":"object"}`}
	if got := declared.Definition(""); got.Description != "declared" || got.Name != "session_list" {
		t.Fatalf("Definition=%#v, want the declared description", got)
	}
	if got := declared.Definition("from the adapter"); got.Description != "from the adapter" {
		t.Fatalf("Definition=%#v, want the adapter description to win", got)
	}
	if schema := declared.Definition("").InputSchema; schema["type"] != "object" {
		t.Fatalf("InputSchema=%#v", schema)
	}
}

// The exception list is closed on purpose: a tool that skips toolgen also skips
// the schema, naming and drift checks, so growing this list has to be a
// deliberate edit with a reason in the PR.
func TestHandWrittenExceptionsAreClosed(t *testing.T) {
	for _, name := range []string{"bash", "view_image", "webfetch", "notify", "goal_control", "code"} {
		if !HandWritten(name) {
			t.Errorf("HandWritten(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"mcp__github__search", "library_search"} {
		if !HandWritten(name) {
			t.Errorf("HandWritten(%q) = false, want true for the prefixed families", name)
		}
	}
	for _, name := range []string{"recally_digest", "goal", "session_list", "memory_search", "shell"} {
		if HandWritten(name) {
			t.Errorf("HandWritten(%q) = true, want a generated tool", name)
		}
	}
	if len(handWritten) != 6 {
		t.Fatalf("hand-written exceptions=%v, want the six named in rules/agent-tools.md §2", handWritten)
	}
}
