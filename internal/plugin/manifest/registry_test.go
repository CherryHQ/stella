package manifest

import (
	"strings"
	"testing"
)

const sampleRegistry = `Tool                          Backends
claude                        aqua:anthropics/claude-code http:claude npm:@anthropic-ai/claude-code
claude-code                   aqua:anthropics/claude-code http:claude npm:@anthropic-ai/claude-code
claude-squad                  aqua:smtg-ai/claude-squad
fd                            aqua:sharkdp/fd cargo:fd-find
ripgrep                       aqua:BurntSushi/ripgrep
`

func TestParseRegistrySkipsHeaderAndSplitsBackends(t *testing.T) {
	tools := parseRegistry(sampleRegistry)
	if len(tools) != 5 {
		t.Fatalf("got %d tools, want 5", len(tools))
	}
	if tools[0].Name != "claude" {
		t.Fatalf("first tool = %q, want claude", tools[0].Name)
	}
	if len(tools[0].Backends) != 3 || tools[0].Backends[2] != "npm:@anthropic-ai/claude-code" {
		t.Fatalf("claude backends = %v, want 3 incl npm key", tools[0].Backends)
	}
}

func TestRankMatchesPrefersNameRelevance(t *testing.T) {
	all := parseRegistry(sampleRegistry)

	got := rankMatches(all, "claude", 30)
	if len(got) != 3 {
		t.Fatalf("got %d matches for 'claude', want 3", len(got))
	}
	if got[0].Name != "claude" {
		t.Fatalf("top match = %q, want exact 'claude' first", got[0].Name)
	}

	// Backend-only match: query hits the backend key, not the name.
	got = rankMatches(all, "burntsushi", 30)
	if len(got) != 1 || got[0].Name != "ripgrep" {
		t.Fatalf("backend search = %v, want [ripgrep]", got)
	}
}

func TestRankMatchesRespectsLimit(t *testing.T) {
	all := parseRegistry(sampleRegistry)
	got := rankMatches(all, "claude", 2)
	if len(got) != 2 {
		t.Fatalf("got %d matches, want capped at 2", len(got))
	}
}

func TestRankMatchesCaseInsensitive(t *testing.T) {
	all := parseRegistry(sampleRegistry)
	if got := rankMatches(all, strings.ToLower("FD"), 30); len(got) != 1 {
		t.Fatalf("got %d matches for 'fd', want 1", len(got))
	}
}
