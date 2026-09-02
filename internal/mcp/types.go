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
	"errors"
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

// Auth types. OAuth is the Authorization Code + PKCE client flow (Phase 3).
const (
	AuthTypeNone   = "none"
	AuthTypeBearer = "bearer"
	AuthTypeOAuth  = "oauth"
)

// Probe health status values, enforced in Go at every write boundary.
const (
	StatusUnknown   = "unknown" // never probed
	StatusOK        = "ok"      // last probe connected and listed tools
	StatusError     = "error"   // last probe or tool call failed (redacted reason in StatusError)
	StatusNeedsAuth = "needs_auth"
)

// Credential modes. shared stores one credential for every user of the
// registration; per_user stores one per user and is only meaningful for OAuth
// (each user connects their own account).
const (
	CredentialModeShared  = "shared"
	CredentialModePerUser = "per_user"
)

// Vault entry name patterns. A v7 UUID is hex + hyphens, so uppercasing and
// swapping hyphens for underscores yields names matching the vault's
// ^[A-Z][A-Z0-9_]{0,127}$ rule with a guaranteed leading letter.
const (
	oauthBundlePrefix       = "MCP_OAUTH_"
	oauthClientSecretPrefix = "MCP_OAUTH_CLIENT_"
)

// ErrVersionConflict tells a Settings caller to read the registration again
// rather than overwrite a concurrent durable change.
var ErrVersionConflict = errors.New("mcp: registration changed; re-read it before retrying")

// ValidTransport reports whether t is an accepted HTTP-based transport.
// It rejects stdio and anything else so a sandbox can never launch a process.
func ValidTransport(t string) bool {
	return t == TransportStreamableHTTP || t == TransportSSE
}

// ValidAuthType reports whether a is a supported auth type.
func ValidAuthType(a string) bool {
	return a == AuthTypeNone || a == AuthTypeBearer || a == AuthTypeOAuth
}

// ValidStatus reports whether s is one of the probe status values.
func ValidStatus(s string) bool {
	switch s {
	case StatusUnknown, StatusOK, StatusError, StatusNeedsAuth:
		return true
	default:
		return false
	}
}

// ValidCredentialMode reports whether m is one of the credential modes.
func ValidCredentialMode(m string) bool {
	return m == CredentialModeShared || m == CredentialModePerUser
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

// CatalogTool is one entry of the persisted tool catalog: a snapshot of what
// the remote server advertised in tools/list at probe time.
type CatalogTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema,omitempty"`
	Annotations map[string]any `json:"annotations,omitempty"`
}

// Registration is one MCP server registration (metadata only, no secret).
type Registration struct {
	ID             string
	Scope          string
	UserID         string
	AgentID        string
	Name           string
	URL            string
	Transport      string
	AuthType       string
	CredentialRef  string // vault entry name holding the bearer token; "" when none
	Enabled        bool
	Status         string
	StatusError    string
	ProbedAt       time.Time // zero when never probed
	Tools          []CatalogTool
	CredentialMode string
	Metadata       map[string]any
	// OAuthClientID is the public pre-registered client id from
	// metadata.oauth.client_id; the client secret never leaves the vault.
	OAuthClientID string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// ToolNamespaceSep separates the MCP prefix, server name, and tool name in the
// namespaced tool id exposed to the model (e.g. mcp__github__create_issue).
const ToolNamespaceSep = "__"

// mcpToolPrefix is the reserved first segment of every MCP tool name.
const mcpToolPrefix = "mcp" + ToolNamespaceSep

// SanitizeIdent normalizes a server or tool name to the [A-Za-z0-9_] charset
// used inside namespaced MCP tool names.
func SanitizeIdent(s, fallback string) string {
	return sanitizeIdent(s, fallback)
}

// SplitToolName splits a namespaced MCP tool name (mcp__<server>__<tool>) into
// its server and tool segments. It splits on the first separator after the
// prefix: a server name containing "__" makes its tools ambiguous, and the
// first split matches how NamespacedToolName composes. ok is false for
// anything that is not a well-formed MCP tool name (missing prefix, missing or
// empty segments, trailing separator).
func SplitToolName(name string) (server, tool string, ok bool) {
	if !strings.HasPrefix(name, mcpToolPrefix) {
		return "", "", false
	}
	rest := strings.TrimPrefix(name, mcpToolPrefix)
	sep := strings.Index(rest, ToolNamespaceSep)
	if sep <= 0 || sep == len(rest)-len(ToolNamespaceSep) {
		return "", "", false
	}
	server, tool = rest[:sep], rest[sep+len(ToolNamespaceSep):]
	if server == "" || tool == "" {
		return "", "", false
	}
	return server, tool, true
}

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

// The hash deliberately covers only user-editable metadata: probe results
// (Status, StatusError, ProbedAt, Tools) are observations, so a probe must
// never change Version() and invalidate a client's If-Match.
func registrationHash(r Registration) [32]byte {
	return sha256.Sum256([]byte(strings.Join([]string{
		r.ID, r.Scope, r.UserID, r.AgentID, r.Name, r.URL, r.Transport,
		r.AuthType, r.CredentialRef, fmt.Sprintf("%t", r.Enabled),
		r.CredentialMode, r.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}, "\x00")))
}

func credentialName(serverID string) string {
	return "MCP_TOKEN_" + strings.ToUpper(strings.ReplaceAll(serverID, "-", "_"))
}

// oauthBundleName is the vault entry holding the OAuth token bundle for one
// registration. For shared mode this is the only bundle; per_user writes the
// same name under each connecting user's user scope.
func oauthBundleName(serverID string) string {
	return oauthBundlePrefix + strings.ToUpper(strings.ReplaceAll(serverID, "-", "_"))
}

// oauthClientSecretName is the vault entry holding the administrator-supplied
// OAuth client secret. The table stores only the public client_id.
func oauthClientSecretName(serverID string) string {
	return oauthClientSecretPrefix + strings.ToUpper(strings.ReplaceAll(serverID, "-", "_"))
}

// validateCredentialMode enforces the credential-mode enum and its coupling:
// per_user is only meaningful for OAuth (each user connects their own
// account). shared stays the default for every auth type.
func validateCredentialMode(mode, authType string) error {
	if mode == "" {
		return nil
	}
	if !ValidCredentialMode(mode) {
		return fmt.Errorf("mcp: invalid credential_mode %q", mode)
	}
	if mode == CredentialModePerUser && authType != AuthTypeOAuth {
		return fmt.Errorf("mcp: credential_mode %q requires auth_type %q", CredentialModePerUser, AuthTypeOAuth)
	}
	return nil
}

// validateRegistration checks the invariants enforced at every write boundary
// (HTTP/CLI): known scope, HTTP-based transport, known auth type, non-empty
// url/name. Enum values are enforced here in Go, not by a DB CHECK.
func validateRegistration(scope, name, rawURL, transport, authType string, policy EndpointPolicy) error {
	if !ValidScope(scope) {
		return fmt.Errorf("mcp: invalid scope %q", scope)
	}
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("mcp: name is required")
	}
	if strings.TrimSpace(rawURL) == "" {
		return fmt.Errorf("mcp: url is required")
	}
	if err := policy.validateEndpointURL(rawURL); err != nil {
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
