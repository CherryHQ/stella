package mcp

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/core/mcpconfig"
	"github.com/CherryHQ/stella/internal/plugin"
	"github.com/CherryHQ/stella/internal/plugin/agentpackage"
)

// RegistrationFromFileResource projects one trusted file resource into the MCP
// registration contract. The caller supplies a FileResource minted by
// plugin.DiscoverResources; ownership is checked again here because this is
// an exported seam used by runtime/session code.
func RegistrationFromFileResource(resource plugin.FileResource, serverKey string, authority authz.Authority) (Registration, error) {
	if err := validateFileResourceAuthority(resource.Key, authority); err != nil {
		return Registration{}, err
	}
	if fileMCPResourceFatal(resource) {
		return Registration{}, fmt.Errorf("mcp: file resource is disabled or invalid")
	}
	if serverKey == "" {
		if resource.Key.Kind == plugin.ResourceMCP {
			serverKey = resource.Key.Name
		} else if len(resource.MCP) == 1 {
			for key := range resource.MCP {
				serverKey = key
			}
		}
	}
	declaration, ok := resource.MCP[serverKey]
	if !ok || serverKey == "" {
		return Registration{}, fmt.Errorf("mcp: file resource has no MCP server %q", serverKey)
	}
	declaration, err := mcpconfig.Normalize(declaration)
	if err != nil {
		return Registration{}, fmt.Errorf("mcp: normalize file MCP declaration: %w", err)
	}
	mode := declaration.Mode
	if mode == CredentialModeShared && (resource.Key.Scope == plugin.ScopeUser || resource.Key.Scope == plugin.ScopeUserAgent) {
		return Registration{}, authz.ErrForbidden
	}
	// Keep the target canonical and independent of package bytes. A description,
	// skill edit, or unrelated server in the same package cannot revoke a grant.
	targetRaw, err := json.Marshal(struct {
		URL            string                   `json:"url"`
		Transport      string                   `json:"transport"`
		Headers        map[string]string        `json:"headers,omitempty"`
		Authentication mcpconfig.Authentication `json:"authentication"`
	}{declaration.URL, declaration.Transport, declaration.Headers, declaration.Authentication})
	if err != nil {
		return Registration{}, fmt.Errorf("mcp: encode file MCP identity: %w", err)
	}
	target := string(targetRaw)
	identityInput := strings.Join([]string{
		string(resource.Key.Scope), resource.Key.UserID, resource.Key.AgentID,
		string(resource.Key.Kind), resource.Key.Name, serverKey, target,
	}, "\x00")
	hash := sha256.Sum256([]byte(identityInput))
	identityBytes := hash[:16]
	identityBytes[6] = (identityBytes[6] & 0x0f) | 8<<4
	identityBytes[8] = (identityBytes[8] & 0x3f) | 0x80
	id := uuid.Must(uuid.FromBytes(identityBytes)).String()
	return Registration{
		IdentityKind: RegistrationIdentityFile,
		FileKey:      resource.Key, AuthenticationTarget: target,
		ID: id, ServerKey: serverKey, PluginID: resource.Key.ID(),
		Scope: string(resource.Key.Scope), UserID: resource.Key.UserID, AgentID: resource.Key.AgentID,
		Name: resource.Key.Name, URL: declaration.URL, Transport: declaration.Transport,
		AuthType: declaration.Type, CredentialRef: declaration.CredentialRef,
		CredentialMode: mode, OAuthClientID: declaration.ClientID,
		OAuthClientSecretRef: declaration.ClientSecretRef, TokenEndpointAuthMethod: declaration.TokenEndpointAuthMethod,
		CallTimeoutSeconds: declaration.CallTimeoutSeconds, OAuthScopes: append([]string(nil), declaration.Scopes...), Headers: cloneHeaders(declaration.Headers),
		// File resources use the canonical scoped key as their package identity.
		// The exported tool name still uses fileToolPackageIdentity so a remote
		// tool cannot collide with a legacy package merely because both are named
		// the same on disk.
		Enabled: true,
	}, nil
}

// fileMCPResourceFatal blocks only resource-level failures. Component-level
// MCP diagnostics are retained so a valid sibling server can still connect.
func fileMCPResourceFatal(resource plugin.FileResource) bool {
	if resource.Disabled || resource.Forbidden || resource.Key.Kind == plugin.ResourcePlugin && resource.Package == nil {
		return true
	}
	for _, diagnostic := range resource.Diagnostics {
		if diagnostic.Severity == agentpackage.SeverityError && (diagnostic.Code == "resource.capture" || diagnostic.Code == "resource.requirement_conflict") {
			return true
		}
	}
	return false
}

func validateFileResourceAuthority(key plugin.ResourceKey, authority authz.Authority) error {
	if !authority.Valid() || authority.Kind() == authz.ActorGuest || key.Name == "" || !ValidScope(string(key.Scope)) {
		return authz.ErrForbidden
	}
	if key.Kind != plugin.ResourcePlugin && key.Kind != plugin.ResourceMCP {
		return authz.ErrForbidden
	}
	switch key.Scope {
	case plugin.ScopeSystem:
		if key.UserID != "" || key.AgentID != "" {
			return authz.ErrForbidden
		}
	case plugin.ScopeSystemAgent:
		if key.UserID != "" || key.AgentID == "" {
			return authz.ErrForbidden
		}
		if authority.Kind() == authz.ActorAgent || authority.Kind() == authz.ActorGroupAgent {
			if string(authority.AgentID()) != key.AgentID {
				return authz.ErrForbidden
			}
		} else if authority.Kind() != authz.ActorUser {
			return authz.ErrForbidden
		}
	case plugin.ScopeUser:
		if key.UserID == "" || key.AgentID != "" {
			return authz.ErrForbidden
		}
		if string(authority.UserID()) != key.UserID || (authority.Kind() != authz.ActorUser && authority.Kind() != authz.ActorAgent) {
			return authz.ErrForbidden
		}
	case plugin.ScopeUserAgent:
		if key.UserID == "" || key.AgentID == "" || string(authority.UserID()) != key.UserID {
			return authz.ErrForbidden
		}
		if authority.Kind() == authz.ActorAgent {
			if string(authority.AgentID()) != key.AgentID {
				return authz.ErrForbidden
			}
		} else if authority.Kind() != authz.ActorUser {
			return authz.ErrForbidden
		}
	default:
		return authz.ErrForbidden
	}
	return nil
}

// FileCredentialOwner checks the caller and returns the exact vault tuple for
// a file-backed registration. Per-user system declarations always use the
// caller's user tuple, never the declaration's empty system owner.
func FileCredentialOwner(reg Registration, authority authz.Authority) (CredentialOwner, error) {
	if !reg.IsFile() || !authority.Valid() {
		return CredentialOwner{}, authz.ErrForbidden
	}
	if authority.Kind() == authz.ActorGuest {
		return CredentialOwner{}, authz.ErrForbidden
	}
	// The registration is an immutable projection of a trusted file key. Check
	// that projection again at every session/credential boundary, since callers
	// may hand us a copied registration without going through discovery.
	if err := validateFileResourceAuthority(reg.FileKey, authority); err != nil ||
		reg.Scope != string(reg.FileKey.Scope) || reg.UserID != reg.FileKey.UserID || reg.AgentID != reg.FileKey.AgentID {
		return CredentialOwner{}, authz.ErrForbidden
	}
	if reg.AuthType == AuthTypeNone {
		return CredentialOwner{Scope: reg.Scope, UserID: reg.UserID, AgentID: reg.AgentID}, nil
	}
	if reg.CredentialMode == CredentialModePerUser {
		if authority.UserID() == "" || (authority.Kind() != authz.ActorUser && authority.Kind() != authz.ActorAgent) {
			return CredentialOwner{}, authz.ErrForbidden
		}
		return CredentialOwner{Scope: ScopeUser, UserID: string(authority.UserID())}, nil
	}
	if reg.Scope == ScopeUser || reg.Scope == ScopeUserAgent {
		if string(authority.UserID()) != reg.UserID || authority.Kind() != authz.ActorAgent && authority.Kind() != authz.ActorUser {
			return CredentialOwner{}, authz.ErrForbidden
		}
	}
	return CredentialOwner{Scope: reg.Scope, UserID: reg.UserID, AgentID: reg.AgentID}, nil
}

var errFileMCPGrantRevoked = errors.New("mcp: file MCP authorization was disconnected")

// FileMCPGrantRevoked lets callers classify a late callback or refresh after
// DisconnectFile rotated the grant generation.
func FileMCPGrantRevoked(err error) bool { return errors.Is(err, errFileMCPGrantRevoked) }

func fileCredentialKey(reg Registration, owner CredentialOwner) string {
	return strings.Join([]string{reg.ID, owner.Scope, owner.UserID, owner.AgentID}, "\x00")
}
