package agentpackage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	maxExportedToolNameBytes = 64
	exportNameHashHexLength  = 12
)

// ExportedToolName maps an Agent Plugin MCP tool tuple to a stable,
// model-facing name. The package name uses the package manifest's validation;
// remote names are accepted as-is and reduced to an ASCII-readable prefix.
// Callers must still detect collisions across the actual exposed tool set.
func ExportedToolName(packageName, serverKey, localToolName string) (string, error) {
	for _, part := range []struct {
		name  string
		value string
	}{
		{"package name", packageName},
		{"server key", serverKey},
		{"local tool name", localToolName},
	} {
		if !utf8.ValidString(part.value) {
			return "", fmt.Errorf("agentpackage: %s must be valid UTF-8", part.name)
		}
	}
	if !ValidName(packageName) {
		return "", fmt.Errorf("agentpackage: invalid package name %q", packageName)
	}
	if serverKey == "" {
		return "", fmt.Errorf("agentpackage: server key must not be empty")
	}
	if localToolName == "" {
		return "", fmt.Errorf("agentpackage: local tool name must not be empty")
	}

	tuple, err := json.Marshal([]string{packageName, serverKey, localToolName})
	if err != nil {
		return "", fmt.Errorf("agentpackage: encode MCP tool identity: %w", err)
	}
	digest := sha256.Sum256(tuple)
	suffix := hex.EncodeToString(digest[:exportNameHashHexLength/2])

	prefix := strings.Join([]string{
		sanitizeExportNameSegment(packageName, "package"),
		sanitizeExportNameSegment(serverKey, "server"),
		sanitizeExportNameSegment(localToolName, "tool"),
	}, "_")
	maxPrefixBytes := maxExportedToolNameBytes - len(suffix) - 1
	if len(prefix) > maxPrefixBytes {
		prefix = prefix[:maxPrefixBytes]
		prefix = strings.TrimRight(prefix, "_-")
	}
	if prefix == "" {
		prefix = "mcp"
	}
	return prefix + "_" + suffix, nil
}

func sanitizeExportNameSegment(value, fallback string) string {
	var builder strings.Builder
	builder.Grow(len(value))
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			builder.WriteRune(r)
		default:
			builder.WriteByte('_')
		}
	}
	segment := strings.Trim(builder.String(), "_-")
	if segment == "" {
		return fallback
	}
	return segment
}
