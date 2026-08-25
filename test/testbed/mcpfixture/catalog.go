// Package mcpfixture owns the public catalog contract shared by the testbed
// supervisor and subprocess system tests. It deliberately contains no routes,
// tokens, schemas, arguments, or tool results.
package mcpfixture

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/CherryHQ/stella/internal/mcp"
)

const (
	ToolCount        = 53
	RegistrationName = "harbor-specialized-fixture"
)

func ToolNames() []string {
	names := []string{"lookup_brief", "transform_brief", "commit_brief"}
	for i := 1; len(names) < ToolCount; i++ {
		names = append(names, fmt.Sprintf("adjacent_catalog_%02d", i))
	}
	return names
}

func NamespacedTools() []string {
	remote := ToolNames()
	out := make([]string, 0, len(remote))
	for _, name := range remote {
		out = append(out, mcp.NamespacedToolName(RegistrationName, name))
	}
	return out
}

func CatalogDigest() string {
	names := NamespacedTools()
	sort.Strings(names)
	sum := sha256.Sum256([]byte(strings.Join(names, "\n")))
	return "sha256:" + hex.EncodeToString(sum[:])
}
