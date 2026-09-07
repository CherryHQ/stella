package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"

	"github.com/jackc/pgx/v5"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/plugin"
)

var (
	// ErrScopeMoveBearerReplacement makes bearer moves fail closed. Copying an
	// old token into a new owner tuple would silently extend its authority.
	ErrScopeMoveBearerReplacement = errors.New("mcp: bearer scope move requires a replacement token")
	ErrScopeMoveOAuth             = errors.New("mcp: OAuth scope moves are unsupported")
	ErrScopeMoveCombinedUpdate    = errors.New("mcp: move scope separately from other updates")
)

// ScopeMoveRequest is the narrow MCP adapter seam for moving one common MCP
// config, including every authored child under a composable parent. Target
// user identity is intentionally absent: Access derives it from the trusted
// authority and checks the target AgentPEP.
type ScopeMoveRequest struct {
	PluginID         string
	ConfigID         string
	ExpectedRevision int64
	TargetScope      plugin.Scope
	TargetAgentID    string
	// Patch carries the optional complete next payload/credential refs needed
	// by a backend validator. Scope moves still preserve auth type and mode.
	Patch       plugin.ConfigPatch
	Replacement *string
	// Replacements is keyed by authored MCP server key. Replacement remains
	// available for the legacy one-child endpoint; a package move uses this
	// map when more than one child needs a bearer token.
	Replacements map[string]string
}

// MoveConfigScope moves one MCP config through common Access and returns its
// metadata adapter. Auth-none moves carry no vault work. Bearer moves delete
// the old exact vault tuple and store the replacement at the CAS-selected
// target tuple in the same transaction. OAuth moves are rejected before any
// config or credential write.
func (s *Service) MoveConfigScope(ctx context.Context, authority authz.Authority, req ScopeMoveRequest) (Registration, error) {
	if s == nil || s.pool == nil || s.plugins == nil || !authority.Valid() || authority.Kind() != authz.ActorUser {
		return Registration{}, authz.ErrForbidden
	}
	if req.PluginID == "" || req.ConfigID == "" || req.ExpectedRevision < 1 {
		return Registration{}, plugin.ErrConflict
	}

	var result Registration
	err := s.plugins.WithMutationTx(ctx, authority, func(mutationCtx context.Context, access *plugin.Access, tx pgx.Tx) error {
		parentID, childKey := req.ConfigID, ""
		linkErr := tx.QueryRow(mutationCtx, `SELECT config_id, server_key FROM plugin_config_mcp_server WHERE id = $1::uuid`, req.ConfigID).Scan(&parentID, &childKey)
		if linkErr != nil && !errors.Is(linkErr, pgx.ErrNoRows) {
			return linkErr
		}
		current, err := access.GetConfig(mutationCtx, req.PluginID, parentID)
		if err != nil {
			return err
		}
		def, err := access.GetDefinition(mutationCtx, current.PluginID)
		if err != nil {
			return err
		}

		currentMerged, err := mergeMCPJSONObjects(def.Spec, current.Payload)
		if err != nil {
			return err
		}
		candidatePayload, err := applyMCPMovePayload(current.Payload, req.Patch)
		if err != nil {
			return err
		}
		candidatePayload, err = mergeMCPJSONObjects(def.Spec, candidatePayload)
		if err != nil {
			return err
		}
		children, err := scopeMoveChildren(current, currentMerged)
		if err != nil {
			return err
		}
		candidatePayloads, err := decodeMCPPluginPayloads(candidatePayload)
		if err != nil {
			return err
		}
		bearerChildren := make([]scopeMoveChild, 0, len(children))
		currentKeys := make(map[string]struct{}, len(children))
		for _, child := range children {
			currentKeys[child.child.ServerKey] = struct{}{}
		}
		if len(candidatePayloads) != len(currentKeys) {
			return fmt.Errorf("%w: scope move cannot add or remove MCP servers", plugin.ErrInvalidConfig)
		}
		for key := range candidatePayloads {
			if _, ok := currentKeys[key]; !ok {
				return fmt.Errorf("%w: scope move cannot add or remove MCP server %q", plugin.ErrInvalidConfig, key)
			}
		}
		for i := range children {
			candidate, ok := candidatePayloads[children[i].child.ServerKey]
			if !ok {
				return fmt.Errorf("mcp: scope move candidate has no server %q", children[i].child.ServerKey)
			}
			if candidate.AuthType != children[i].payload.AuthType || candidate.CredentialMode != children[i].payload.CredentialMode {
				return errors.New("mcp: scope move cannot change auth type or credential mode")
			}
			switch children[i].payload.AuthType {
			case AuthTypeNone:
			case AuthTypeBearer:
				bearerChildren = append(bearerChildren, children[i])
			case AuthTypeOAuth:
				return ErrScopeMoveOAuth
			default:
				return errors.New("mcp: unsupported auth type for scope move")
			}
		}
		if len(bearerChildren) == 0 {
			if req.Replacement != nil || len(req.Replacements) != 0 || req.Patch.CredentialRefsSet && len(req.Patch.CredentialRefs) != 0 {
				return authz.ErrForbidden
			}
		} else {
			if s.bindVault == nil {
				return errPluginCredentialsUnavailable
			}
			if req.Patch.CredentialRefsSet || (req.Replacement != nil && req.Replacements != nil) {
				return authz.ErrForbidden
			}
		}
		replacements, err := scopeMoveReplacements(req, children, bearerChildren)
		if err != nil {
			return err
		}
		req.Patch.CredentialRefsSet = true
		req.Patch.CredentialRefs, err = scopeMoveCredentialRefs(current.CredentialRefs, children, req.TargetScope, req.TargetAgentID, authority)
		if err != nil {
			return err
		}
		if req.Patch.BinaryVersionsSet {
			return authz.ErrForbidden
		}

		moved, err := access.MoveConfig(permittedConfigMutation(mutationCtx, tx, current, plugin.MutationMove, false), req.PluginID, parentID, req.ExpectedRevision, req.TargetScope, req.TargetAgentID, req.Patch)
		if err != nil {
			return err
		}
		newOwner := CredentialOwner{Scope: string(moved.Scope), UserID: moved.UserID, AgentID: moved.AgentID}
		var vault Vault
		if len(bearerChildren) != 0 {
			vault = s.bindVault(tx)
			if vault == nil {
				return errPluginCredentialsUnavailable
			}
		}
		for _, child := range bearerChildren {
			replacement := replacements[child.child.ServerKey]
			mutation := CredentialMutation{tx: tx, config: moved, registrationID: child.child.ID, serverKey: child.child.ServerKey, owner: newOwner, vault: vault, configManaged: true}
			if err := mutation.StoreBearer(mutationCtx, replacement); err != nil {
				return err
			}
		}

		effective, err := plugin.Resolve(def, []plugin.Config{moved}, moved.UserID, moved.AgentID)
		if err != nil {
			return err
		}
		if childKey != "" {
			for _, child := range moved.MCPServers {
				if child.ID == req.ConfigID {
					result, err = RegistrationFromPluginChild(def, moved, effective, child, PluginMCPObservation{ConfigRevision: moved.Revision}, authority)
					return err
				}
			}
			return authz.ErrNotFound
		}
		if len(moved.MCPServers) > 1 {
			// A package parent has no single endpoint to return. Preserve the
			// requested parent identity while the caller can enumerate children.
			result = Registration{ID: moved.ID, ParentConfigID: moved.ID, PluginID: moved.PluginID, ConfigRevision: moved.Revision, Scope: string(moved.Scope), UserID: moved.UserID, AgentID: moved.AgentID, Name: def.DisplayName, Enabled: effective.IsEffectivelyEnabled, CreatedAt: moved.CreatedAt.UTC(), UpdatedAt: moved.UpdatedAt.UTC()}
			return nil
		}
		result, err = RegistrationFromPluginConfig(def, moved, effective, PluginMCPObservation{ConfigRevision: moved.Revision}, authority)
		return err
	})
	return result, err
}

type scopeMoveChild struct {
	child   plugin.MCPServerChild
	payload mcpPluginPayload
	linked  bool
}

func scopeMoveChildren(config plugin.Config, merged json.RawMessage) ([]scopeMoveChild, error) {
	if len(config.MCPServers) == 0 {
		payload, err := decodeMCPPluginPayloadSingle(merged)
		if err != nil {
			return nil, err
		}
		return []scopeMoveChild{{child: plugin.MCPServerChild{ID: config.ID, ParentConfigID: config.ID, ServerKey: "main"}, payload: payload}}, nil
	}
	children := make([]scopeMoveChild, 0, len(config.MCPServers))
	for _, child := range config.MCPServers {
		payload, err := decodeMCPPluginPayloadForKey(merged, child.ServerKey)
		if err != nil {
			return nil, err
		}
		children = append(children, scopeMoveChild{child: child, payload: payload, linked: true})
	}
	return children, nil
}

func scopeMoveReplacements(req ScopeMoveRequest, children, bearerChildren []scopeMoveChild) (map[string]string, error) {
	if len(bearerChildren) == 0 {
		return nil, nil
	}
	if req.Replacement != nil && req.Replacements == nil && len(children) == 1 {
		if *req.Replacement == "" {
			return nil, ErrScopeMoveBearerReplacement
		}
		return map[string]string{bearerChildren[0].child.ServerKey: *req.Replacement}, nil
	}
	if req.Replacements == nil {
		return nil, ErrScopeMoveBearerReplacement
	}
	known := make(map[string]struct{}, len(bearerChildren))
	for _, child := range bearerChildren {
		known[child.child.ServerKey] = struct{}{}
	}
	for key := range req.Replacements {
		if _, ok := known[key]; !ok {
			return nil, authz.ErrForbidden
		}
	}
	result := make(map[string]string, len(bearerChildren))
	for _, child := range bearerChildren {
		replacement, ok := req.Replacements[child.child.ServerKey]
		if !ok || replacement == "" {
			return nil, ErrScopeMoveBearerReplacement
		}
		result[child.child.ServerKey] = replacement
	}
	return result, nil
}

func scopeMoveCredentialRefs(currentRefs json.RawMessage, children []scopeMoveChild, targetScope plugin.Scope, targetAgentID string, authority authz.Authority) (json.RawMessage, error) {
	refsObject, err := decodeJSONObject(currentRefs, "MCP credential refs")
	if err != nil {
		return nil, err
	}
	if len(children) == 1 && !children[0].linked {
		if children[0].payload.AuthType == AuthTypeBearer {
			refsObject["bearer"] = bearerMoveRefs(children[0].child.ID, targetScope, targetAgentID, authority)
		} else {
			delete(refsObject, "bearer")
		}
		delete(refsObject, "oauth_bundle")
		delete(refsObject, "oauth_client_secret")
		return json.Marshal(refsObject)
	}
	childRefs := make(map[string]json.RawMessage, len(children))
	for _, child := range children {
		if child.payload.AuthType == AuthTypeBearer {
			childRefs[child.child.ServerKey] = bearerMoveRefs(child.child.ID, targetScope, targetAgentID, authority)
		} else {
			childRefs[child.child.ServerKey] = json.RawMessage(`{}`)
		}
	}
	refsObject["mcp_servers"], err = json.Marshal(childRefs)
	if err != nil {
		return nil, err
	}
	return json.Marshal(refsObject)
}

// applyMCPMovePayload mirrors common ConfigPatch payload semantics locally so
// the adapter can classify auth before invoking the mutating Access method.
func applyMCPMovePayload(current json.RawMessage, patch plugin.ConfigPatch) (json.RawMessage, error) {
	if !patch.PayloadSet && len(patch.ResetFields) == 0 {
		return current, nil
	}
	if len(current) == 0 {
		current = json.RawMessage(`{}`)
	}
	var owned map[string]json.RawMessage
	if err := json.Unmarshal(current, &owned); err != nil || owned == nil {
		return nil, plugin.ErrInvalidConfig
	}
	fields := map[string]json.RawMessage{}
	if patch.PayloadSet {
		if err := json.Unmarshal(patch.Payload, &fields); err != nil || fields == nil {
			return nil, plugin.ErrInvalidConfig
		}
		maps.Copy(owned, fields)
	}
	for _, key := range patch.ResetFields {
		if key == "" {
			return nil, plugin.ErrInvalidConfig
		}
		if _, supplied := fields[key]; supplied {
			return nil, fmt.Errorf("%w: patch and reset overlap", plugin.ErrInvalidConfig)
		}
		delete(owned, key)
	}
	return json.Marshal(owned)
}

func bearerMoveRefs(configID string, targetScope plugin.Scope, targetAgentID string, authority authz.Authority) json.RawMessage {
	userID := ""
	if targetScope == plugin.ScopeUser || targetScope == plugin.ScopeUserAgent {
		userID = string(authority.UserID())
	}
	ref := map[string]string{
		"name": credentialName(configID), "scope": string(targetScope),
		"user_id": userID, "agent_id": targetAgentID,
	}
	raw, _ := json.Marshal(map[string]any{"bearer": ref})
	return raw
}
