package mcp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"sync/atomic"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/plugin"
	vaultpkg "github.com/CherryHQ/stella/internal/vault"
)

var errTypedConfigMutationRequired = fmt.Errorf("%w: mcp: connection identity changes require the typed credential mutation", plugin.ErrInvalidConfig)

type configMutationPermitKey struct{}

type configMutationPermit struct {
	tx          pgx.Tx
	configID    string
	pluginID    string
	revision    int64
	kind        plugin.MutationKind
	forceRevoke bool
	revokeIDs   map[string]struct{}
	used        atomic.Bool
}

// The permit exists only for one synchronous common write inside a typed MCP
// transaction. Callbacks receiving Access do not inherit this capability.
func permittedConfigMutation(ctx context.Context, tx pgx.Tx, before plugin.Config, kind plugin.MutationKind, forceRevoke bool) context.Context {
	return context.WithValue(ctx, configMutationPermitKey{}, &configMutationPermit{
		tx: tx, configID: before.ID, pluginID: before.PluginID, revision: before.Revision, kind: kind, forceRevoke: forceRevoke,
	})
}

func permittedChildConfigMutation(ctx context.Context, tx pgx.Tx, before plugin.Config, kind plugin.MutationKind, forceRevoke bool, childID string) context.Context {
	ctx = permittedConfigMutation(ctx, tx, before, kind, forceRevoke)
	permit := ctx.Value(configMutationPermitKey{}).(*configMutationPermit)
	if childID != "" {
		permit.revokeIDs = map[string]struct{}{childID: {}}
	}
	return ctx
}

func NewMCPBackendPolicy(endpointPolicy EndpointPolicy) plugin.BackendPolicy {
	return plugin.BackendPolicy{
		Validate:   NewMCPPayloadValidator(endpointPolicy),
		Transition: transitionMCPConfig,
	}
}

// This runs after a successful row CAS, before commit. The row lock also fences
// refresh; any failure rolls back the config and its credential changes.
func transitionMCPConfig(ctx context.Context, tx pgx.Tx, authority authz.Authority, kind plugin.MutationKind, def plugin.Definition, before, after *plugin.Config) error {
	if tx == nil || !authority.Valid() {
		return authz.ErrForbidden
	}
	if (before == nil || !payloadHasMCP(before.Payload)) && (after == nil || (!definitionHasMCP(def) && !payloadHasMCP(after.Payload))) {
		return nil
	}
	if kind == plugin.MutationDelete {
		if before == nil || after != nil {
			return authz.ErrForbidden
		}
		return cleanupMCPCredentials(ctx, tx, *before, nil)
	}
	if after == nil {
		return authz.ErrForbidden
	}
	if before == nil {
		// Typed creation starts with a validated auth-none row, then installs the
		// final refs and secret in this same transaction.
		if len(after.Payload) == 0 {
			return nil
		}
		merged, err := mergeMCPJSONObjects(def.Spec, after.Payload)
		if err != nil {
			return err
		}
		payloads, err := decodeMCPPluginPayloads(merged)
		if err != nil {
			return err
		}
		for _, payload := range payloads {
			if payload.AuthType == AuthTypeBearer {
				return errTypedConfigMutationRequired
			}
		}
		return nil
	}
	permit, hasPermit := ctx.Value(configMutationPermitKey{}).(*configMutationPermit)
	forceRevoke := hasPermit && permit.forceRevoke
	// Closing an obsolete config must remain possible without decoding it.
	if !forceRevoke && before.Scope == after.Scope && before.UserID == after.UserID && before.AgentID == after.AgentID && bytes.Equal(before.Payload, after.Payload) && bytes.Equal(before.CredentialRefs, after.CredentialRefs) {
		return nil
	}
	oldIdentities, err := mcpExecutionIdentities(def, *before)
	if err != nil {
		return err
	}
	newIdentities, err := mcpExecutionIdentities(def, *after)
	if err != nil {
		return err
	}
	changed := changedMCPCredentialIDs(oldIdentities, newIdentities, *before)
	if forceRevoke && len(changed) == 0 && hasPermit {
		for id := range permit.revokeIDs {
			changed[id] = struct{}{}
		}
		if len(changed) == 0 && len(before.MCPServers) == 0 {
			changed[before.ID] = struct{}{}
		}
	}
	if hasPermit && len(permit.revokeIDs) != 0 {
		for id := range changed {
			if _, allowed := permit.revokeIDs[id]; !allowed {
				return fmt.Errorf("%w: MCP child mutation permit does not cover child %q", plugin.ErrInvalidConfig, id)
			}
		}
	}
	if !forceRevoke && len(changed) == 0 {
		return nil
	}
	if !hasPermit || !reflect.ValueOf(tx).Comparable() || !reflect.ValueOf(permit.tx).Comparable() || permit.tx != tx || permit.configID != before.ID || permit.pluginID != before.PluginID || permit.revision != before.Revision || permit.kind != kind || !permit.used.CompareAndSwap(false, true) {
		return errTypedConfigMutationRequired
	}
	return cleanupMCPCredentialIDs(ctx, tx, changed)
}

// cleanupMCPCredentials removes the old child namespaces while the parent row
// is still locked. A flat config keeps its parent UUID as the credential key.
func cleanupMCPCredentials(ctx context.Context, tx pgx.Tx, before plugin.Config, after *plugin.Config) error {
	ids := make(map[string]struct{})
	if len(before.MCPServers) == 0 {
		ids[before.ID] = struct{}{}
	} else {
		for _, child := range before.MCPServers {
			ids[child.ID] = struct{}{}
		}
	}
	if after != nil {
		for _, child := range after.MCPServers {
			delete(ids, child.ID)
		}
	}
	return cleanupMCPCredentialIDs(ctx, tx, ids)
}

func cleanupMCPCredentialIDs(ctx context.Context, tx pgx.Tx, ids map[string]struct{}) error {
	for id := range ids {
		uuidID, err := uuid.Parse(id)
		if err != nil {
			return authz.ErrForbidden
		}
		if err := vaultpkg.DeleteMCPConfigCredentialsTx(ctx, tx, uuidID); err != nil {
			return err
		}
	}
	return nil
}

func changedMCPCredentialIDs(old, next map[string]mcpConnectionIdentity, before plugin.Config) map[string]struct{} {
	changed := make(map[string]struct{})
	for key, oldIdentity := range old {
		newIdentity, ok := next[key]
		if !ok || oldIdentity != newIdentity {
			if id := mcpChildID(before, key); id != "" {
				changed[id] = struct{}{}
			} else {
				changed[before.ID] = struct{}{}
			}
		}
	}
	return changed
}

func mcpChildID(cfg plugin.Config, key string) string {
	for _, child := range cfg.MCPServers {
		if child.ServerKey == key {
			return child.ID
		}
	}
	return ""
}

type mcpConnectionIdentity struct {
	Scope                                                                       plugin.Scope
	UserID, AgentID                                                             string
	URL, Transport, AuthType, CredentialMode, ClientID, TokenEndpointAuthMethod string
	Refs                                                                        [2]string
}

// mcpExecutionPayloadDigest excludes presentation-only fields such as
// description. Credential cleanup is keyed by the connection identity, so a
// harmless label edit must not revoke a live bearer/OAuth secret.
func mcpExecutionPayloadDigest(payloads map[string]mcpPluginPayload) [32]byte {
	type executionPayload struct {
		Key            string            `json:"key"`
		URL            string            `json:"url"`
		Transport      string            `json:"transport"`
		AuthType       string            `json:"auth_type"`
		CredentialMode string            `json:"credential_mode"`
		Headers        map[string]string `json:"headers,omitempty"`
		Metadata       map[string]any    `json:"metadata,omitempty"`
	}
	keys := make([]string, 0, len(payloads))
	for key := range payloads {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	values := make([]executionPayload, 0, len(keys))
	for _, key := range keys {
		payload := payloads[key]
		values = append(values, executionPayload{Key: key, URL: payload.URL, Transport: payload.Transport, AuthType: payload.AuthType, CredentialMode: payload.CredentialMode, Headers: payload.Headers, Metadata: payload.Metadata})
	}
	raw, _ := json.Marshal(values)
	return sha256.Sum256(raw)
}

func mcpExecutionIdentity(def plugin.Definition, cfg plugin.Config) (mcpConnectionIdentity, error) {
	identity := mcpConnectionIdentity{Scope: cfg.Scope, UserID: cfg.UserID, AgentID: cfg.AgentID}
	if len(cfg.Payload) == 0 {
		return identity, nil
	}
	merged, err := mergeMCPJSONObjects(def.Spec, cfg.Payload)
	if err != nil {
		return identity, err
	}
	payloads, err := decodeMCPPluginPayloads(merged)
	if err != nil {
		return identity, err
	}
	payloadDigest := mcpExecutionPayloadDigest(payloads)
	refsDigest := sha256.Sum256(cfg.CredentialRefs)
	identity.Refs = [2]string{fmt.Sprintf("%x", payloadDigest[:]), fmt.Sprintf("%x", refsDigest[:])}
	for key, payload := range payloads {
		if identity.URL == "" {
			identity.URL, identity.Transport, identity.AuthType, identity.CredentialMode = payload.URL, payload.Transport, payload.AuthType, payload.CredentialMode
			identity.ClientID = metadataOAuthClientID(payload.Metadata)
			identity.TokenEndpointAuthMethod = oauthMetadataTokenEndpointAuthMethod(payload.Metadata)
		} else if identity.AuthType != payload.AuthType || identity.CredentialMode != payload.CredentialMode {
			identity.AuthType, identity.CredentialMode = "multi", "multi"
		}
		if key != "main" {
			identity.URL = "multi"
		}
	}

	return identity, nil
}

// mcpExecutionIdentities keeps credential identity per authored child. A
// sibling endpoint edit therefore revokes only that child's Vault namespace.
func mcpExecutionIdentities(def plugin.Definition, cfg plugin.Config) (map[string]mcpConnectionIdentity, error) {
	// A negative scope config is an explicit empty resource set. It must not
	// merge the definition's MCP defaults and invent a phantom "main" child
	// while the first authored child is being added.
	if len(cfg.Payload) == 0 {
		return map[string]mcpConnectionIdentity{}, nil
	}
	merged, err := mergeMCPJSONObjects(def.Spec, cfg.Payload)
	if err != nil {
		return nil, err
	}
	if !payloadHasMCP(merged) {
		return map[string]mcpConnectionIdentity{}, nil
	}
	object, err := decodeJSONObject(merged, "MCP config payload")
	if err != nil {
		return nil, err
	}
	servers, err := decodeJSONObject(object["mcp_servers"], "MCP mcp_servers payload")
	if err != nil {
		return nil, err
	}
	if len(servers) == 0 {
		// An empty composable set has no credential identity. Only a real
		// legacy flat endpoint keeps the parent UUID as its namespace.
		if _, hasURL := object["url"]; !hasURL {
			return map[string]mcpConnectionIdentity{}, nil
		}
		identity, err := mcpExecutionIdentity(def, cfg)
		if err != nil {
			return nil, err
		}
		return map[string]mcpConnectionIdentity{"main": identity}, nil
	}
	refsObject := map[string]json.RawMessage{}
	if len(cfg.CredentialRefs) != 0 {
		refsObject, err = decodeJSONObject(cfg.CredentialRefs, "MCP credential refs")
		if err != nil {
			return nil, err
		}
	}
	refsChildren := map[string]json.RawMessage{}
	if raw, ok := refsObject["mcp_servers"]; ok {
		refsChildren, err = decodeJSONObject(raw, "MCP credential refs mcp_servers")
		if err != nil {
			return nil, err
		}
	}
	result := make(map[string]mcpConnectionIdentity, len(servers))
	legacyDigest := mcpLegacyExecutionFieldsDigest(merged)
	for key, raw := range servers {
		payload, err := decodeMCPPluginChildPayload(raw)
		if err != nil {
			return nil, fmt.Errorf("MCP server %q: %w", key, err)
		}
		childRefs := refsChildren[key]
		if len(childRefs) == 0 {
			childRefs = json.RawMessage(`{}`)
		}
		payloadDigest := mcpExecutionPayloadDigest(map[string]mcpPluginPayload{key: payload})
		refsDigest := sha256.Sum256(childRefs)
		result[key] = mcpConnectionIdentity{
			Scope: cfg.Scope, UserID: cfg.UserID, AgentID: cfg.AgentID,
			URL: payload.URL, Transport: payload.Transport, AuthType: payload.AuthType,
			CredentialMode: payload.CredentialMode, ClientID: metadataOAuthClientID(payload.Metadata),
			TokenEndpointAuthMethod: oauthMetadataTokenEndpointAuthMethod(payload.Metadata),
			Refs:                    [2]string{fmt.Sprintf("%x:%s", payloadDigest[:], legacyDigest), fmt.Sprintf("%x", refsDigest[:])},
		}
	}
	return result, nil
}

func mcpLegacyExecutionFieldsDigest(raw json.RawMessage) string {
	object, err := decodeJSONObject(raw, "MCP config payload")
	if err != nil {
		return ""
	}
	fields := make(map[string]json.RawMessage)
	for _, key := range []string{"url", "transport", "auth_type", "credential_mode"} {
		if value, ok := object[key]; ok {
			fields[key] = value
		}
	}
	if len(fields) == 0 {
		return ""
	}
	encoded, _ := json.Marshal(fields)
	digest := sha256.Sum256(encoded)
	return fmt.Sprintf("%x", digest[:])
}

func updateCredentialConfig(ctx context.Context, access *plugin.Access, tx pgx.Tx, current plugin.Config, forceRevoke bool, patch plugin.ConfigPatch) (plugin.Config, error) {
	return access.UpdateConfig(permittedConfigMutation(ctx, tx, current, plugin.MutationUpdate, forceRevoke), current.PluginID, current.ID, current.Revision, patch)
}

func updateCredentialChildConfig(ctx context.Context, access *plugin.Access, tx pgx.Tx, current plugin.Config, childID string, forceRevoke bool, patch plugin.ConfigPatch) (plugin.Config, error) {
	return access.UpdateConfig(permittedChildConfigMutation(ctx, tx, current, plugin.MutationUpdate, forceRevoke, childID), current.PluginID, current.ID, current.Revision, patch)
}
