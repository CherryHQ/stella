// Package mcp is an MCP (Model Context Protocol) client for Stella. It lets
// agents connect to external MCP servers over HTTP-based transports only
// (streamable HTTP + SSE); stdio is deliberately unsupported so the
// multi-user sandbox boundary never spawns local processes.
//
// Registrations are scoped exactly like skills and vault entries
// (system / system_agent / user / user_agent) and any auth credential is stored
// age-encrypted in the vault, never in this table.
package mcp

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"time"
)

// Scope values mirror the skill/vault 4-value model.
const (
	ScopeUser        = "user"
	ScopeUserAgent   = "user_agent"
	ScopeSystem      = "system"
	ScopeSystemAgent = "system_agent"
)

// Transport values. HTTP-based only; stdio is intentionally absent.
const (
	// TransportStreamableHTTP is the streamable HTTP transport (2025 spec).
	TransportStreamableHTTP = "streamable_http"
	// TransportSSE is the HTTP + Server-Sent Events transport (2024-11-05 spec).
	TransportSSE = "sse"
)

// Auth types. OAuth is deferred: store an encrypted bearer token per connection.
const (
	AuthTypeNone   = "none"
	AuthTypeBearer = "bearer"
)

// ValidTransport reports whether t is an accepted HTTP-based transport.
// It rejects stdio and anything else so a sandbox can never launch a process.
func ValidTransport(t string) bool {
	return t == TransportStreamableHTTP || t == TransportSSE
}

// ValidAuthType reports whether a is a supported auth type.
func ValidAuthType(a string) bool {
	return a == AuthTypeNone || a == AuthTypeBearer
}

// ValidScope reports whether s is one of the 4 scope values.
func ValidScope(s string) bool {
	switch s {
	case ScopeUser, ScopeUserAgent, ScopeSystem, ScopeSystemAgent:
		return true
	default:
		return false
	}
}

// IsSystemScope reports whether a scope is admin-managed (no owning user).
func IsSystemScope(scope string) bool {
	return scope == ScopeSystem || scope == ScopeSystemAgent
}

// Registration is one MCP server registration (metadata only, no secret).
type Registration struct {
	ID            string
	Scope         string
	UserID        string
	AgentID       string
	Name          string
	URL           string
	Transport     string
	AuthType      string
	CredentialRef string // vault entry name holding the bearer token; "" when none
	Enabled       bool
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// ToolNamespaceSep separates the MCP prefix, server name, and tool name in the
// namespaced tool id exposed to the model (e.g. mcp__github__create_issue).
const ToolNamespaceSep = "__"

// NamespacedToolName returns the agent-facing tool name for a remote MCP tool,
// namespaced by server so tools from different servers do not collide with core,
// plugin, or skill tools. Both server and remote tool segments are normalized to
// [A-Za-z0-9_]; callers still detect collisions between normalized names.
func NamespacedToolName(serverName, toolName string) string {
	return "mcp" + ToolNamespaceSep + sanitizeIdent(serverName, "server") + ToolNamespaceSep + sanitizeIdent(toolName, "tool")
}

func sanitizeIdent(s, fallback string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		return fallback
	}
	return out
}

// credentialName derives a deterministic, valid vault entry name for a server's
// bearer token. A v7 UUID is hex + hyphens, so uppercasing and swapping hyphens
// for underscores yields a name matching the vault's ^[A-Z][A-Z0-9_]{0,127}$
// rule (the MCP_TOKEN_ prefix guarantees a leading letter).
func registrationHash(r Registration) [32]byte {
	return sha256.Sum256([]byte(strings.Join([]string{
		r.ID, r.Scope, r.UserID, r.AgentID, r.Name, r.URL, r.Transport,
		r.AuthType, r.CredentialRef, fmt.Sprintf("%t", r.Enabled), r.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}, "\x00")))
}

func credentialName(serverID string) string {
	return "MCP_TOKEN_" + strings.ToUpper(strings.ReplaceAll(serverID, "-", "_"))
}

// validateRegistration checks the invariants enforced at every write boundary
// (HTTP/CLI): known scope, HTTP-based transport, known auth type, non-empty
// url/name. Enum values are enforced here in Go, not by a DB CHECK.
func validateRegistration(scope, name, rawURL, transport, authType string) error {
	if !ValidScope(scope) {
		return fmt.Errorf("mcp: invalid scope %q", scope)
	}
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("mcp: name is required")
	}
	if strings.TrimSpace(rawURL) == "" {
		return fmt.Errorf("mcp: url is required")
	}
	if err := validateEndpointURL(rawURL); err != nil {
		return err
	}
	if !ValidTransport(transport) {
		return fmt.Errorf("mcp: unsupported transport %q: only %q and %q are allowed (stdio is not supported)", transport, TransportStreamableHTTP, TransportSSE)
	}
	if !ValidAuthType(authType) {
		return fmt.Errorf("mcp: invalid auth_type %q", authType)
	}
	return nil
}
