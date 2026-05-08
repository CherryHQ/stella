package mcp

import (
	"fmt"
	"maps"
	"strings"
	"sync"
	"unicode"
)

const toolIDPrefix = "mcp__"

// Target identifies one original MCP tool on one MCP server.
type Target struct {
	ServerName string
	ToolName   string
}

// CanonicalRegistry maintains a stable mapping from canonical Stella MCP tool IDs
// to the original MCP server/tool names.
type CanonicalRegistry struct {
	mu        sync.RWMutex
	byID      map[string]Target
	baseCount map[string]int
}

func NewCanonicalRegistry() *CanonicalRegistry {
	return &CanonicalRegistry{
		byID:      map[string]Target{},
		baseCount: map[string]int{},
	}
}

func SanitizeName(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "unnamed"
	}
	return b.String()
}

func BaseToolID(serverName, toolName string) string {
	return toolIDPrefix + SanitizeName(serverName) + "__" + SanitizeName(toolName)
}

func ParseToolID(id string) (Target, error) {
	if !strings.HasPrefix(id, toolIDPrefix) {
		return Target{}, fmt.Errorf("invalid mcp tool id %q", id)
	}
	parts := strings.Split(id[len(toolIDPrefix):], "__")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return Target{}, fmt.Errorf("invalid mcp tool id %q", id)
	}
	return Target{ServerName: parts[0], ToolName: parts[1]}, nil
}

func (r *CanonicalRegistry) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byID = map[string]Target{}
	r.baseCount = map[string]int{}
}

func (r *CanonicalRegistry) Add(serverName, toolName string) string {
	r.mu.Lock()
	defer r.mu.Unlock()

	target := Target{ServerName: serverName, ToolName: toolName}
	base := BaseToolID(serverName, toolName)
	if existing, ok := r.byID[base]; !ok || existing == target {
		r.byID[base] = target
		if _, ok := r.baseCount[base]; !ok {
			r.baseCount[base] = 1
		}
		return base
	}

	for i := r.baseCount[base] + 1; ; i++ {
		candidate := fmt.Sprintf("%s__%d", base, i)
		if existing, ok := r.byID[candidate]; !ok || existing == target {
			r.byID[candidate] = target
			r.baseCount[base] = i
			return candidate
		}
	}
}

func (r *CanonicalRegistry) Resolve(id string) (Target, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	target, ok := r.byID[id]
	return target, ok
}

func (r *CanonicalRegistry) Snapshot() map[string]Target {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]Target, len(r.byID))
	maps.Copy(out, r.byID)
	return out
}
