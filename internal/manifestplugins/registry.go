package manifestplugins

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
)

// RegistryTool is one entry from `mise registry`: a short name and the backend
// keys that can provide it (e.g. "aqua:anthropics/claude-code",
// "npm:@anthropic-ai/claude-code"). Either the short name or a backend key is a
// valid mise tool spec for a manifest binary.
type RegistryTool struct {
	Name     string   `json:"name"`
	Backends []string `json:"backends"`
}

var (
	registryCacheMu   sync.Mutex
	registryCache     []RegistryTool
	registryCacheDone bool
)

// loadRegistry runs `mise registry` once and caches the parsed result. The
// registry is static data baked into the mise binary, so a process-lifetime
// cache is safe and avoids re-spawning mise on every search.
func loadRegistry(ctx context.Context, stellaHome string) ([]RegistryTool, error) {
	registryCacheMu.Lock()
	defer registryCacheMu.Unlock()
	if registryCacheDone {
		return registryCache, nil
	}

	out, err := runMiseCapture(ctx, stellaHome, "registry")
	if err != nil {
		return nil, err
	}
	registryCache = parseRegistry(out)
	registryCacheDone = true
	return registryCache, nil
}

// parseRegistry parses `mise registry` output. Each line is a tool name followed
// by whitespace-separated backend keys; a leading "Tool ..." header is ignored.
func parseRegistry(out string) []RegistryTool {
	var tools []RegistryTool
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 || fields[0] == "Tool" {
			continue
		}
		tools = append(tools, RegistryTool{Name: fields[0], Backends: fields[1:]})
	}
	return tools
}

// SearchRegistry returns registry tools whose name or backend keys contain query
// (case-insensitive), ranked by name relevance. An empty query returns nothing —
// the registry has thousands of entries and is meant to be filtered. Results are
// capped at limit (default 30).
func SearchRegistry(ctx context.Context, stellaHome, query string, limit int) ([]RegistryTool, error) {
	query = strings.TrimSpace(strings.ToLower(query))
	if query == "" {
		return nil, nil
	}
	all, err := loadRegistry(ctx, stellaHome)
	if err != nil {
		return nil, err
	}
	return rankMatches(all, query, limit), nil
}

// rankMatches filters tools matching query (in name or a backend key) and orders
// them by name relevance: exact, then prefix, then substring, then backend-only —
// so the most relevant tool (e.g. "claude") floats to the top. query must be
// lowercased. Results are capped at limit (default 30).
func rankMatches(all []RegistryTool, query string, limit int) []RegistryTool {
	if limit <= 0 {
		limit = 30
	}
	var matches []RegistryTool
	for _, t := range all {
		if queryRank(t, query) < 3 || backendMatches(t, query) {
			matches = append(matches, t)
		}
	}
	sort.SliceStable(matches, func(i, j int) bool {
		return queryRank(matches[i], query) < queryRank(matches[j], query)
	})
	if len(matches) > limit {
		matches = matches[:limit]
	}
	return matches
}

func queryRank(t RegistryTool, query string) int {
	name := strings.ToLower(t.Name)
	switch {
	case name == query:
		return 0
	case strings.HasPrefix(name, query):
		return 1
	case strings.Contains(name, query):
		return 2
	default:
		return 3
	}
}

func backendMatches(t RegistryTool, query string) bool {
	for _, b := range t.Backends {
		if strings.Contains(strings.ToLower(b), query) {
			return true
		}
	}
	return false
}

// LatestVersion resolves the latest installable version for a mise tool key
// (short name like "fd" or backend-qualified like "github:cli/cli"). It may hit
// the network to query the backend's available versions.
func LatestVersion(ctx context.Context, stellaHome, tool string) (string, error) {
	tool = strings.TrimSpace(tool)
	if tool == "" {
		return "", fmt.Errorf("tool is required")
	}
	out, err := runMiseCapture(ctx, stellaHome, "latest", tool)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// runMiseCapture runs a read-only mise subcommand in a neutral cwd (so no
// ambient project mise.toml is picked up) under the isolated mise env and
// returns its stdout. It installs nothing.
func runMiseCapture(ctx context.Context, stellaHome string, args ...string) (string, error) {
	miseBin, err := findMiseBin(stellaHome)
	if err != nil {
		return "", err
	}
	env, err := isolatedMiseEnv(stellaHome)
	if err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp("", "stella-mise-query-*")
	if err != nil {
		return "", fmt.Errorf("create temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	var stdout, stderr bytes.Buffer
	cmd := ManagedCommandContext(ctx, miseBin, args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("mise %s: %w\nstderr: %s", strings.Join(args, " "), err, stderr.String())
	}
	return stdout.String(), nil
}
