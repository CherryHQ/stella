//go:build capability

package capabilities

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

var webFullPathPattern = regexp.MustCompile(`fullPath:\s*'([^']+)'`)

// RepositorySurfaces contains the public surfaces extracted from the current
// checkout. CLI commands are supplied by the cmd/stellad package test because a
// package main cannot be imported by the report command.
type RepositorySurfaces struct {
	OpenAPI      []string `json:"openapi"`
	WebRoutes    []string `json:"web_routes"`
	CLICommands  []string `json:"cli_commands,omitempty"`
	Plugins      []string `json:"plugins"`
	SystemSkills []string `json:"system_skills"`
}

// TestMetrics records repository-wide facts that help interpret the existing
// asset inventory without confusing file counts with capability coverage.
type TestMetrics struct {
	GoTestFiles             int `json:"go_test_files"`
	FrontendTestFiles       int `json:"frontend_test_files"`
	SystemTestFiles         int `json:"system_test_files"`
	HTTPTestFiles           int `json:"httptest_files"`
	DBTestFiles             int `json:"dbtest_files"`
	MemoryTestFiles         int `json:"memorytest_files"`
	TestifyDirectTestFiles  int `json:"testify_direct_test_files"`
	TestcontainersTestFiles int `json:"testcontainers_test_files"`
}

// CollectRepositorySurfaces extracts all surfaces that can be discovered
// without starting Stella. pluginIDs must come from the real builtin catalog.
func CollectRepositorySurfaces(root string, pluginIDs []string) (RepositorySurfaces, error) {
	openAPI, err := collectOpenAPIOperations(root)
	if err != nil {
		return RepositorySurfaces{}, err
	}
	webRoutes, err := collectWebRoutes(root)
	if err != nil {
		return RepositorySurfaces{}, err
	}
	systemSkills, err := collectSystemSkills(root)
	if err != nil {
		return RepositorySurfaces{}, err
	}
	return RepositorySurfaces{
		OpenAPI:      openAPI,
		WebRoutes:    webRoutes,
		Plugins:      sortedUnique(pluginIDs),
		SystemSkills: systemSkills,
	}, nil
}

func collectOpenAPIOperations(root string) ([]string, error) {
	files, err := filepath.Glob(filepath.Join(root, "api", "spec", "domain", "*", "paths.yaml"))
	if err != nil {
		return nil, fmt.Errorf("glob OpenAPI domain paths: %w", err)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no OpenAPI domain path files found")
	}

	var operations []string
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		var document yaml.Node
		if err := yaml.Unmarshal(data, &document); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		collectYAMLValues(&document, "operationId", &operations)
	}
	return sortedUnique(operations), nil
}

// collectYAMLValues walks mappings and sequences because operation objects may
// move within a domain file while retaining the same public operationId.
func collectYAMLValues(node *yaml.Node, key string, values *[]string) {
	if node.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(node.Content); i += 2 {
			if node.Content[i].Value == key {
				*values = append(*values, node.Content[i+1].Value)
			}
			collectYAMLValues(node.Content[i+1], key, values)
		}
		return
	}
	for _, child := range node.Content {
		collectYAMLValues(child, key, values)
	}
}

func collectWebRoutes(root string) ([]string, error) {
	path := filepath.Join(root, "web", "src", "routeTree.gen.ts")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read generated web route tree: %w", err)
	}
	matches := webFullPathPattern.FindAllStringSubmatch(string(data), -1)
	if len(matches) == 0 {
		return nil, fmt.Errorf("no fullPath entries found in %s", path)
	}
	routes := make([]string, 0, len(matches))
	for _, match := range matches {
		routes = append(routes, match[1])
	}
	// Layout and index nodes can share a fullPath. Capability coverage owns the
	// public URL once, rather than counting generated implementation nodes.
	return sortedUnique(routes), nil
}

func collectSystemSkills(root string) ([]string, error) {
	dir := filepath.Join(root, "resources", "skills", "system")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read system skill directory: %w", err)
	}
	var skills []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, entry.Name(), "SKILL.md")); err == nil {
			skills = append(skills, entry.Name())
		}
	}
	return sortedUnique(skills), nil
}

// CollectTestMetrics scans source files only; generated dependencies and build
// artifacts are excluded so the inventory remains stable across local setups.
func CollectTestMetrics(root string) (TestMetrics, error) {
	var metrics TestMetrics
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "dist", "node_modules":
				if path != root {
					return filepath.SkipDir
				}
			}
			return nil
		}

		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		name := entry.Name()
		if isFrontendTestFile(name) && strings.HasPrefix(rel, "web/") {
			metrics.FrontendTestFiles++
		}
		if !strings.HasSuffix(name, "_test.go") {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		content := string(data)
		// The inventory describes the tests that existed before this checker.
		// Excluding its tagged self-tests prevents the act of measuring from
		// inflating the recorded baseline.
		if strings.Contains(content, "//go:build capability") {
			return nil
		}
		metrics.GoTestFiles++
		if strings.HasPrefix(rel, "test/system/") {
			metrics.SystemTestFiles++
		}
		if strings.Contains(content, `"net/http/httptest"`) {
			metrics.HTTPTestFiles++
		}
		if strings.Contains(content, "internal/db/dbtest") {
			metrics.DBTestFiles++
		}
		if strings.Contains(content, "internal/memory/memorytest") {
			metrics.MemoryTestFiles++
		}
		if strings.Contains(content, "github.com/stretchr/testify") {
			metrics.TestifyDirectTestFiles++
		}
		if strings.Contains(strings.ToLower(content), "testcontainers") {
			metrics.TestcontainersTestFiles++
		}
		return nil
	})
	if err != nil {
		return TestMetrics{}, fmt.Errorf("scan test metrics: %w", err)
	}
	return metrics, nil
}

func isFrontendTestFile(name string) bool {
	for _, marker := range []string{".test.", ".spec."} {
		if strings.Contains(name, marker) {
			return true
		}
	}
	return false
}

func sortedUnique(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok || value == "" {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
