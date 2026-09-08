// Package mcpconfig defines non-secret MCP declarations shared by file readers
// and the MCP client. Network resolution and authorization remain in MCP.
package mcpconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
)

type Authentication struct {
	Type                    string   `json:"auth_type,omitempty"`
	Mode                    string   `json:"credential_mode,omitempty"`
	CredentialRef           string   `json:"credential_ref,omitempty"`
	ClientID                string   `json:"client_id,omitempty"`
	ClientSecretRef         string   `json:"client_secret_ref,omitempty"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method,omitempty"`
	Scopes                  []string `json:"scopes,omitempty"`
}

type Declaration struct {
	URL                string            `json:"url"`
	Transport          string            `json:"transport"`
	Headers            map[string]string `json:"headers,omitempty"`
	CallTimeoutSeconds int               `json:"call_timeout_seconds,omitzero"`
	Authentication
}

func Parse(data []byte) (Declaration, error) {
	var declaration Declaration
	if err := decodeDeclaration(data, &declaration); err != nil {
		return Declaration{}, err
	}
	return Normalize(declaration)
}

func ParseAuthentication(data []byte) (Authentication, error) {
	var auth Authentication
	if err := decodeDeclaration(data, &auth); err != nil {
		return Authentication{}, err
	}
	return normalizeAuthentication(auth)
}

func decodeDeclaration(data []byte, target any) error {
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || fields == nil {
		return errors.New("mcp: declaration must be an object")
	}
	for _, raw := range fields {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return errors.New("mcp: declaration fields must not be null")
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errors.New("mcp: invalid declaration fields")
	}
	return nil
}

func Normalize(d Declaration) (Declaration, error) {
	if d.CallTimeoutSeconds < 0 || d.CallTimeoutSeconds > 300 {
		return Declaration{}, errors.New("mcp: call timeout must be between 1 and 300 seconds")
	}
	if !ValidEndpoint(d.URL) || !ValidHeaders(d.Headers) {
		return Declaration{}, errors.New("mcp: invalid endpoint or public headers")
	}
	if d.Transport != "streamable_http" && d.Transport != "sse" {
		return Declaration{}, errors.New("mcp: unsupported transport")
	}
	var err error
	d.Authentication, err = normalizeAuthentication(d.Authentication)
	if err != nil {
		return Declaration{}, err
	}
	if d.Type == "oauth" && d.Transport != "streamable_http" {
		return Declaration{}, errors.New("mcp: OAuth requires streamable HTTP")
	}
	return d, nil
}

func normalizeAuthentication(d Authentication) (Authentication, error) {
	if d.Type == "" {
		d.Type = "none"
	}
	if d.Type != "none" && d.Type != "bearer" && d.Type != "oauth" {
		return Authentication{}, errors.New("mcp: unsupported authentication")
	}
	if d.Mode == "" {
		d.Mode = "per_user"
	}
	if d.Mode != "per_user" && d.Mode != "shared" {
		return Authentication{}, errors.New("mcp: unsupported credential mode")
	}
	if d.Type != "oauth" && (d.ClientID != "" || d.ClientSecretRef != "" || len(d.Scopes) > 0 || d.TokenEndpointAuthMethod != "") {
		return Authentication{}, errors.New("mcp: client configuration requires OAuth")
	}
	switch d.TokenEndpointAuthMethod {
	case "", "none", "client_secret_basic", "client_secret_post":
	default:
		return Authentication{}, errors.New("mcp: unsupported token endpoint authentication method")
	}
	if d.Type == "none" && d.CredentialRef != "" {
		return Authentication{}, errors.New("mcp: credential reference requires authentication")
	}
	for _, ref := range []string{d.CredentialRef, d.ClientSecretRef} {
		if ref == "" {
			continue
		}
		if len(ref) > 128 || ref[0] < 'A' || ref[0] > 'Z' {
			return Authentication{}, errors.New("mcp: invalid credential reference")
		}
		for _, r := range ref {
			if (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' {
				return Authentication{}, errors.New("mcp: invalid credential reference")
			}
		}
	}
	return d, nil
}

// ValidEndpoint rejects secret-bearing URL components before diagnostics or
// receipts can expose them. The transport also validates DNS and resolved IPs.
func ValidEndpoint(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Hostname() != "" && u.User == nil && u.RawQuery == "" && u.Fragment == ""
}

func ValidHeaders(headers map[string]string) bool {
	seen := map[string]bool{}
	for key, value := range headers {
		lower := strings.ToLower(key)
		if seen[lower] || key == "" {
			return false
		}
		seen[lower] = true
		switch lower {
		case "authorization", "proxy-authorization", "cookie", "set-cookie", "x-api-key", "api-key":
			return false
		}
		for _, r := range key {
			if r <= 0x20 || r >= 0x7f || strings.ContainsRune("()<>@,;:\\\"/[]?={} \t", r) {
				return false
			}
		}
		for _, r := range value {
			if (r < 0x20 && r != '\t') || r == 0x7f {
				return false
			}
		}
	}
	return true
}
