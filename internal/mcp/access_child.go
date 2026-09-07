package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"

	"github.com/jackc/pgx/v5"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/plugin"
)

func storeChildSecret(ctx context.Context, vault Vault, owner CredentialOwner, name, value string) error {
	if vault == nil || name == "" || value == "" {
		return errPluginCredentialsUnavailable
	}
	if IsSystemScope(owner.Scope) {
		return vault.SetSystemScoped(ctx, owner.Scope, owner.AgentID, name, value)
	}
	return vault.SetScoped(ctx, owner.Scope, owner.UserID, owner.AgentID, name, value)
}

// parentConfig returns the authored parent and its child identities. It is
// deliberately separate from Registration projection because a composable
// parent has no single endpoint to project.
func (a *Access) parentConfig(ctx context.Context, parentID string) (plugin.Config, plugin.Definition, error) {
	if a == nil || a.svc == nil || a.svc.plugins == nil {
		return plugin.Config{}, plugin.Definition{}, authz.ErrForbidden
	}
	access, err := a.svc.plugins.Begin(a.authority)
	if err != nil {
		return plugin.Config{}, plugin.Definition{}, err
	}
	var cfg plugin.Config
	var def plugin.Definition
	// The parent ID is itself the authorization boundary. GetConfig derives
	// owner identity from the bound authority and never trusts request tuples.
	defs, err := access.ListDefinitions(ctx)
	if err != nil {
		return plugin.Config{}, plugin.Definition{}, err
	}
	for _, candidate := range defs {
		cfg, err = access.GetConfig(ctx, candidate.ID, parentID)
		if err == nil {
			def = candidate
			return cfg, def, nil
		}
		if !errors.Is(err, plugin.ErrNotFound) {
			return plugin.Config{}, plugin.Definition{}, err
		}
	}
	return plugin.Config{}, plugin.Definition{}, authz.ErrNotFound
}

// ListChildren lists only children belonging to one authorized parent.
func (a *Access) ListChildren(ctx context.Context, parentConfigID string) ([]Registration, error) {
	cfg, def, err := a.parentConfig(ctx, parentConfigID)
	if err != nil {
		return nil, err
	}
	effective, err := plugin.Resolve(def, []plugin.Config{cfg}, cfg.UserID, cfg.AgentID)
	if err != nil {
		return nil, err
	}
	result := make([]Registration, 0, len(cfg.MCPServers))
	for _, child := range cfg.MCPServers {
		observation, err := a.svc.commonObservation(ctx, cfg, effective.Payload, child.ID, a.authority)
		if err != nil {
			return nil, err
		}
		reg, err := RegistrationFromPluginChild(def, cfg, effective, child, observation, a.authority)
		if err != nil {
			return nil, err
		}
		result = append(result, reg)
	}
	return result, nil
}

// CreateChild adds one authored server under an existing parent. The parent
// revision is the sole CAS token; child enabled state is inherited from it.
func (a *Access) CreateChild(ctx context.Context, parentConfigID, serverKey string, expectedParentRevision int64, in CreateInput) (Registration, error) {
	if expectedParentRevision < 1 || serverKey == "" {
		return Registration{}, plugin.ErrConflict
	}
	if in.AuthType == "" {
		in.AuthType = AuthTypeNone
	}
	if in.CredentialMode == "" {
		in.CredentialMode = CredentialModeShared
	}
	if in.Transport == "" {
		in.Transport = TransportStreamableHTTP
	}
	cfg, def, err := a.parentConfig(ctx, parentConfigID)
	if err != nil {
		return Registration{}, err
	}
	if cfg.Revision != expectedParentRevision {
		return Registration{}, plugin.ErrConflict
	}
	declaration, err := plugin.DecodeResourcePayload(def.Spec, "MCP definition spec")
	if err != nil {
		return Registration{}, err
	}
	_, declared := declaration.MCPServers[serverKey]
	// The parent is the only trusted owner tuple. Never derive child locators
	// from request fields, which are intentionally absent from the API contract.
	in.Scope, in.UserID, in.AgentID = string(cfg.Scope), cfg.UserID, cfg.AgentID
	for _, child := range cfg.MCPServers {
		if child.ServerKey == serverKey {
			return Registration{}, fmt.Errorf("mcp: server key %q already exists", serverKey)
		}
	}
	parameters, err := decodeMCPConfigParameters(cfg.Payload)
	if err != nil {
		return Registration{}, err
	}
	if parameters.MCPServers == nil {
		parameters.MCPServers = make(map[string]plugin.MCPParameters)
	}
	if _, exists := parameters.MCPServers[serverKey]; exists {
		return Registration{}, fmt.Errorf("mcp: server key %q already exists", serverKey)
	}
	refsObject := map[string]json.RawMessage{}
	if len(cfg.CredentialRefs) != 0 {
		refsObject, err = decodeJSONObject(cfg.CredentialRefs, "MCP credential refs")
		if err != nil {
			return Registration{}, err
		}
	}
	allChildRefs := map[string]json.RawMessage{}
	if nested, ok := refsObject["mcp_servers"]; ok {
		allChildRefs, err = decodeJSONObject(nested, "MCP credential refs mcp_servers")
		if err != nil {
			return Registration{}, err
		}
	}
	var updated plugin.Config
	childID := ""
	err = a.svc.withPluginMutation(ctx, a.authority, func(mutationCtx context.Context, access *plugin.Access, tx pgx.Tx) error {
		var err error
		if !declared {
			currentDef, getErr := access.GetDefinition(mutationCtx, def.ID)
			if getErr != nil {
				return getErr
			}
			server := plugin.MCPServerResource{
				URL: in.URL, Transport: in.Transport, AuthType: in.AuthType,
				CredentialMode: in.CredentialMode, Metadata: in.Metadata,
			}
			if in.Description != nil {
				server.Description = *in.Description
			}
			def, err = access.EnsureMCPServerDeclaration(mutationCtx, currentDef.ID, currentDef.Revision, serverKey, server)
			if err != nil {
				return err
			}
		}
		createdChild, createErr := access.CreateMCPServerChild(mutationCtx, cfg.ID, serverKey)
		childID, err = createdChild.ID, createErr
		if err != nil {
			return err
		}
		payload, childRefs, err := commonMCPCreatePayload(childID, in)
		if err != nil {
			return err
		}
		childParameters, err := mcpParametersFromPayload(payload)
		if err != nil {
			return err
		}
		parameters.MCPServers[serverKey] = childParameters
		updatedRaw, err := json.Marshal(parameters)
		if err != nil {
			return err
		}
		childRefsObject, err := decodeJSONObject(childRefs, "MCP child credential refs")
		if err != nil {
			return err
		}
		childRefsRaw, err := json.Marshal(childRefsObject)
		if err != nil {
			return err
		}
		allChildRefs[serverKey] = childRefsRaw
		refsNestedRaw, err := json.Marshal(allChildRefs)
		if err != nil {
			return err
		}
		refsObject["mcp_servers"] = refsNestedRaw
		updatedRefs, err := json.Marshal(refsObject)
		if err != nil {
			return err
		}
		updated, err = access.UpdateConfig(mutationCtx, def.ID, cfg.ID, expectedParentRevision, plugin.ConfigPatch{PayloadSet: true, Payload: updatedRaw, CredentialRefsSet: true, CredentialRefs: updatedRefs})
		if err != nil {
			return err
		}
		vault := Vault(nil)
		if a.svc.bindVault != nil {
			vault = a.svc.bindVault(tx)
		}
		owner := CredentialOwner{Scope: string(updated.Scope), UserID: updated.UserID, AgentID: updated.AgentID}
		if in.Token != "" {
			if in.AuthType != AuthTypeBearer {
				return fmt.Errorf("mcp: token requires bearer auth")
			}
			if err := storeChildSecret(mutationCtx, vault, owner, credentialName(childID), in.Token); err != nil {
				return err
			}
		}
		if in.OAuthClientSecret != "" {
			if in.AuthType != AuthTypeOAuth {
				return fmt.Errorf("mcp: OAuth client secret requires OAuth auth")
			}
			if err := storeChildSecret(mutationCtx, vault, owner, oauthClientSecretName(childID), in.OAuthClientSecret); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return Registration{}, err
	}
	for _, child := range updated.MCPServers {
		if child.ServerKey != serverKey {
			continue
		}
		effective, err := plugin.Resolve(def, []plugin.Config{updated}, updated.UserID, updated.AgentID)
		if err != nil {
			return Registration{}, err
		}
		return RegistrationFromPluginChild(def, updated, effective, child, PluginMCPObservation{ConfigRevision: updated.Revision}, a.authority)
	}
	return Registration{}, authz.ErrNotFound
}

// UpdateChild applies endpoint/auth metadata and credential changes to one
// child while CASing the parent. The parent revision remains the sole token;
// the child UUID namespaces all Vault and credential refs.
func (a *Access) UpdateChild(ctx context.Context, id string, expectedParentRevision int64, in UpdateInput) (Registration, error) {
	reg, err := a.GetVisible(ctx, id)
	if err != nil {
		return Registration{}, err
	}
	if reg.ConfigRevision != expectedParentRevision {
		return Registration{}, plugin.ErrConflict
	}
	cfg, def, err := a.parentConfig(ctx, reg.ParentConfigID)
	if err != nil {
		return Registration{}, err
	}
	effective, err := plugin.Resolve(def, []plugin.Config{cfg}, cfg.UserID, cfg.AgentID)
	if err != nil {
		return Registration{}, err
	}
	effectiveObject, err := decodeJSONObject(effective.Payload, "MCP config payload")
	if err != nil {
		return Registration{}, err
	}
	effectiveServers, err := decodeJSONObject(effectiveObject["mcp_servers"], "MCP mcp_servers payload")
	if err != nil {
		return Registration{}, err
	}
	effectiveChild, ok := effectiveServers[reg.ServerKey]
	if !ok {
		return Registration{}, authz.ErrNotFound
	}
	parameters, err := decodeMCPConfigParameters(cfg.Payload)
	if err != nil {
		return Registration{}, err
	}
	childParameters, ok := parameters.MCPServers[reg.ServerKey]
	if !ok {
		declaration, decodeErr := plugin.DecodeResourcePayload(def.Spec, "MCP definition spec")
		if decodeErr != nil {
			return Registration{}, decodeErr
		}
		if declaration.Origin == "remote_mcp" {
			return Registration{}, authz.ErrNotFound
		}
		// Fixed packages inherit endpoint parameters from the declaration. An
		// update materializes only the edited child into the scope config while
		// preserving the package's immutable resource membership.
		childParameters, err = mcpParametersFromPayload(effectiveChild)
		if err != nil {
			return Registration{}, err
		}
		if parameters.MCPServers == nil {
			parameters.MCPServers = make(map[string]plugin.MCPParameters)
		}
	}
	currentPayload, err := decodeMCPPluginChildPayload(effectiveChild)
	if err != nil {
		return Registration{}, err
	}
	authType := currentPayload.AuthType
	if in.AuthType != nil {
		authType = *in.AuthType
	}
	transport := currentPayload.Transport
	if in.Transport != nil {
		transport = *in.Transport
	}
	endpoint := currentPayload.URL
	if in.URL != nil {
		endpoint = *in.URL
	}
	mode := currentPayload.CredentialMode
	if in.CredentialMode != nil {
		mode = *in.CredentialMode
	}
	if !ValidAuthType(authType) || !ValidTransport(transport) || !ValidCredentialMode(mode) {
		return Registration{}, fmt.Errorf("mcp: invalid child connection settings")
	}
	if err := validateCredentialMode(mode, authType); err != nil {
		return Registration{}, err
	}
	if err := validateRegistration(reg.Scope, reg.Name, endpoint, transport, authType, a.svc.endpoints); err != nil {
		return Registration{}, err
	}
	if authType == AuthTypeBearer && (in.Token == nil && reg.AuthType != AuthTypeBearer) {
		return Registration{}, fmt.Errorf("%w: mcp: enabling bearer auth requires a replacement token", plugin.ErrInvalidConfig)
	}
	if authType == AuthTypeBearer && in.Token != nil && *in.Token == "" {
		return Registration{}, fmt.Errorf("%w: mcp: bearer token must not be empty", plugin.ErrInvalidConfig)
	}
	oldSecretRef := ""
	if _, _, oldSecretRef, err = decodeMCPPluginCredentialRefsForKey(cfg.CredentialRefs, plugin.Config{ID: reg.ID, Scope: cfg.Scope, UserID: cfg.UserID, AgentID: cfg.AgentID}, reg.ServerKey, currentPayload.AuthType, currentPayload.CredentialMode); err != nil && currentPayload.AuthType != AuthTypeNone {
		return Registration{}, err
	}
	nextMetadata := cloneMetadata(currentPayload.Metadata)
	if in.Metadata != nil {
		nextMetadata = *in.Metadata
	}
	nextClientID := metadataOAuthClientID(nextMetadata)
	if in.OAuthClientID != nil {
		nextClientID = *in.OAuthClientID
	}
	if in.OAuthClientSecret != nil && *in.OAuthClientSecret != "" && (authType != AuthTypeOAuth || nextClientID == "") {
		return Registration{}, fmt.Errorf("%w: mcp: OAuth client secret requires a client id", plugin.ErrInvalidConfig)
	}
	sensitiveEdit := endpoint != currentPayload.URL || transport != currentPayload.Transport || authType != currentPayload.AuthType || mode != currentPayload.CredentialMode || nextClientID != metadataOAuthClientID(currentPayload.Metadata) || in.OAuthClientSecret != nil
	if authType == AuthTypeOAuth && sensitiveEdit && oldSecretRef != "" && in.OAuthClientSecret == nil && nextClientID != "" {
		return Registration{}, fmt.Errorf("%w: mcp: OAuth connection changes require a replacement client secret or clearing the client id", plugin.ErrInvalidConfig)
	}
	if authType == AuthTypeBearer && sensitiveEdit && in.Token == nil {
		return Registration{}, fmt.Errorf("%w: mcp: bearer endpoint changes require a replacement token", plugin.ErrInvalidConfig)
	}
	childParameters.URL = endpoint
	childParameters.Transport = transport
	childParameters.AuthType = authType
	childParameters.CredentialMode = mode
	childParameters.Metadata = nextMetadata
	if in.Description != nil {
		description := *in.Description
		childParameters.Description = &description
	}
	if in.OAuthClientID != nil {
		metadata := cloneMetadata(nextMetadata)
		if metadata == nil {
			metadata = make(map[string]any)
		}
		oauthMetadata, ok := metadata["oauth"].(map[string]any)
		if !ok {
			oauthMetadata = map[string]any{}
		}
		oauthMetadata["client_id"] = *in.OAuthClientID
		metadata["oauth"] = oauthMetadata
		childParameters.Metadata = metadata
	}
	parameters.MCPServers[reg.ServerKey] = childParameters
	keepClientSecret := oldSecretRef != "" && in.OAuthClientSecret == nil && nextClientID != ""
	if in.OAuthClientSecret != nil && *in.OAuthClientSecret != "" {
		keepClientSecret = true
	}
	childRefs, err := childCredentialRefs(reg.ID, cfg, authType, mode, nextClientID, keepClientSecret)
	if err != nil {
		return Registration{}, err
	}
	refsObject, err := decodeJSONObject(cfg.CredentialRefs, "MCP credential refs")
	if err != nil {
		return Registration{}, err
	}
	refsChildren := map[string]json.RawMessage{}
	if nested, ok := refsObject["mcp_servers"]; ok {
		refsChildren, err = decodeJSONObject(nested, "MCP credential refs mcp_servers")
		if err != nil {
			return Registration{}, err
		}
	} else if len(refsObject) != 0 {
		return Registration{}, errors.New("MCP credential refs is missing mcp_servers")
	}
	refsChildren[reg.ServerKey] = childRefs
	refsObject["mcp_servers"], err = json.Marshal(refsChildren)
	if err != nil {
		return Registration{}, err
	}
	delete(refsObject, "bearer")
	delete(refsObject, "oauth_bundle")
	delete(refsObject, "oauth_client_secret")
	updatedRefs, err := json.Marshal(refsObject)
	if err != nil {
		return Registration{}, err
	}
	updatedRaw, err := json.Marshal(parameters)
	if err != nil {
		return Registration{}, err
	}
	var updated plugin.Config
	err = a.svc.withPluginMutation(ctx, a.authority, func(mutationCtx context.Context, access *plugin.Access, tx pgx.Tx) error {
		var err error
		patch := plugin.ConfigPatch{PayloadSet: true, Payload: updatedRaw, CredentialRefsSet: true, CredentialRefs: updatedRefs}
		if in.Enabled != nil || in.EnabledSet {
			patch.EnabledSet, patch.Enabled = true, in.Enabled
		}
		updated, err = updateCredentialChildConfig(mutationCtx, access, tx, cfg, reg.ID, authType == AuthTypeOAuth && in.OAuthClientSecret != nil, patch)
		if err != nil {
			return err
		}
		if in.Name != nil {
			currentDef, defErr := access.GetDefinition(mutationCtx, updated.PluginID)
			if defErr != nil {
				return defErr
			}
			if *in.Name != currentDef.DisplayName {
				if _, defErr = access.UpdateDefinition(mutationCtx, updated.PluginID, currentDef.Revision, plugin.DefinitionPatch{DisplayName: in.Name}); defErr != nil {
					return defErr
				}
				def.DisplayName = *in.Name
			}
		}
		vault := Vault(nil)
		if a.svc.bindVault != nil {
			vault = a.svc.bindVault(tx)
		}
		owner := CredentialOwner{Scope: string(updated.Scope), UserID: updated.UserID, AgentID: updated.AgentID}
		if in.Token != nil {
			if authType != AuthTypeBearer {
				return fmt.Errorf("mcp: token requires bearer auth")
			}
			if err := storeChildSecret(mutationCtx, vault, owner, credentialName(reg.ID), *in.Token); err != nil {
				return err
			}
		}
		if in.OAuthClientSecret != nil && *in.OAuthClientSecret != "" {
			if authType != AuthTypeOAuth {
				return fmt.Errorf("mcp: OAuth client secret requires OAuth auth")
			}
			if err := storeChildSecret(mutationCtx, vault, owner, oauthClientSecretName(reg.ID), *in.OAuthClientSecret); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return Registration{}, err
	}
	for _, updatedChild := range updated.MCPServers {
		if updatedChild.ID != reg.ID {
			continue
		}
		effective, err := plugin.Resolve(def, []plugin.Config{updated}, updated.UserID, updated.AgentID)
		if err != nil {
			return Registration{}, err
		}
		return RegistrationFromPluginChild(def, updated, effective, updatedChild, PluginMCPObservation{ConfigRevision: updated.Revision}, a.authority)
	}
	return Registration{}, authz.ErrNotFound
}

func childCredentialRefs(childID string, cfg plugin.Config, authType, mode, clientID string, keepClientSecret bool) (json.RawMessage, error) {
	refs := map[string]any{}
	switch authType {
	case AuthTypeNone:
		return json.Marshal(refs)
	case AuthTypeBearer:
		refs["bearer"] = map[string]string{"name": credentialName(childID), "scope": string(cfg.Scope), "user_id": cfg.UserID, "agent_id": cfg.AgentID}
	case AuthTypeOAuth:
		bundle := map[string]string{"name": oauthBundleName(childID), "mode": mode}
		if mode == CredentialModePerUser {
			bundle["owner"] = "per_user"
		} else {
			bundle["scope"], bundle["user_id"], bundle["agent_id"] = string(cfg.Scope), cfg.UserID, cfg.AgentID
		}
		refs["oauth_bundle"] = bundle
		if clientID != "" && keepClientSecret {
			refs["oauth_client_secret"] = map[string]string{"name": oauthClientSecretName(childID), "scope": string(cfg.Scope), "user_id": cfg.UserID, "agent_id": cfg.AgentID}
		}
	default:
		return nil, fmt.Errorf("mcp: unsupported child auth type %q", authType)
	}
	return json.Marshal(refs)
}

func decodeMCPConfigParameters(raw json.RawMessage) (plugin.ConfigParameters, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return plugin.ConfigParameters{}, nil
	}
	return plugin.DecodeConfigParameters(raw)
}

func mcpParametersFromPayload(raw json.RawMessage) (plugin.MCPParameters, error) {
	payload, err := decodeMCPPluginChildPayload(raw)
	if err != nil {
		return plugin.MCPParameters{}, err
	}
	var description *string
	object, err := decodeJSONObject(raw, "MCP child payload")
	if err != nil {
		return plugin.MCPParameters{}, err
	}
	if value, ok := object["description"]; ok {
		var decoded string
		if err := json.Unmarshal(value, &decoded); err != nil {
			return plugin.MCPParameters{}, fmt.Errorf("mcp child payload description: %w", err)
		}
		description = &decoded
	}
	return plugin.MCPParameters{
		Description:    description,
		URL:            payload.URL,
		Transport:      payload.Transport,
		AuthType:       payload.AuthType,
		CredentialMode: payload.CredentialMode,
		Headers:        payload.Headers,
		Metadata:       payload.Metadata,
	}, nil
}

func cloneMetadata(metadata map[string]any) map[string]any {
	cloned := maps.Clone(metadata)
	if oauth, ok := cloned["oauth"].(map[string]any); ok {
		cloned["oauth"] = maps.Clone(oauth)
	}
	return cloned
}

// DeleteChild removes one authored child and CASes its parent configuration.
func (a *Access) DeleteChild(ctx context.Context, id string, expectedParentRevision int64) error {
	reg, err := a.GetVisible(ctx, id)
	if err != nil {
		return err
	}
	if reg.ConfigRevision != expectedParentRevision {
		return plugin.ErrConflict
	}
	cfg, def, err := a.parentConfig(ctx, reg.ParentConfigID)
	if err != nil {
		return err
	}
	declaration, err := plugin.DecodeResourcePayload(def.Spec, "MCP definition spec")
	if err != nil {
		return err
	}
	if declaration.Origin != "remote_mcp" {
		return authz.ErrForbidden
	}
	parameters, err := decodeMCPConfigParameters(cfg.Payload)
	if err != nil {
		return err
	}
	if _, ok := parameters.MCPServers[reg.ServerKey]; !ok {
		return authz.ErrNotFound
	}
	delete(parameters.MCPServers, reg.ServerKey)
	// Keep the empty map explicit. Config patches merge top-level fields, so
	// omitting mcp_servers would leave the previous selection intact.
	if len(parameters.MCPServers) == 0 {
		parameters.MCPServers = map[string]plugin.MCPParameters{}
	}
	updatedRaw, err := json.Marshal(parameters)
	if err != nil {
		return err
	}
	updatedRefs := cfg.CredentialRefs
	if len(updatedRefs) != 0 {
		refsObject, decodeErr := decodeJSONObject(updatedRefs, "MCP credential refs")
		if decodeErr != nil {
			return decodeErr
		}
		if nested, ok := refsObject["mcp_servers"]; ok {
			children, decodeErr := decodeJSONObject(nested, "MCP credential refs mcp_servers")
			if decodeErr != nil {
				return decodeErr
			}
			delete(children, reg.ServerKey)
			refsObject["mcp_servers"], decodeErr = json.Marshal(children)
			if decodeErr != nil {
				return decodeErr
			}
			updatedRefs, decodeErr = json.Marshal(refsObject)
			if decodeErr != nil {
				return decodeErr
			}
		}
	}
	patch := plugin.ConfigPatch{PayloadSet: true, Payload: updatedRaw, CredentialRefsSet: true, CredentialRefs: updatedRefs}
	return a.svc.withPluginMutation(ctx, a.authority, func(mutationCtx context.Context, access *plugin.Access, tx pgx.Tx) error {
		if _, err := updateCredentialChildConfig(mutationCtx, access, tx, cfg, reg.ID, false, patch); err != nil {
			return err
		}
		return access.DeleteMCPServerChild(mutationCtx, cfg.ID, reg.ID)
	})
}
