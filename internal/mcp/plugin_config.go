package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/plugin"
)

// PluginMCPObservation contains the probe state kept outside plugin_config.
// It is deliberately separate from the backend payload: status and tools are
// remote observations, not authored configuration.
type PluginMCPObservation struct {
	Status           string
	StatusError      string
	ProbedAt         time.Time
	ConfigRevision   int64
	CredentialUserID string
	Tools            []CatalogTool
}

// RegistrationFromPluginConfig adapts one resolved MCP plugin config to the
// legacy service shape while the MCP service is being migrated. The config ID
// remains the registration UUID; credential references are locators only and
// are never decoded as secret values.
func RegistrationFromPluginConfig(def plugin.Definition, cfg plugin.Config, effective plugin.Effective, observation PluginMCPObservation, authority authz.Authority) (Registration, error) {
	if !authority.Valid() {
		return Registration{}, authz.ErrForbidden
	}
	if err := def.Validate(); err != nil {
		return Registration{}, fmt.Errorf("mcp plugin definition: %w", err)
	}
	if !definitionHasMCP(def) && !payloadHasMCP(cfg.Payload) {
		return Registration{}, errors.New("plugin definition has no MCP resources")
	}
	if err := cfg.Validate(); err != nil {
		return Registration{}, fmt.Errorf("mcp plugin config: %w", err)
	}
	if cfg.PluginID != def.ID {
		return Registration{}, errors.New("mcp plugin config does not match its definition")
	}
	if effective.PluginID != def.ID || effective.ConfigID != cfg.ID {
		return Registration{}, errors.New("mcp effective config does not match its source config")
	}
	if effective.SourceScope != cfg.Scope {
		return Registration{}, errors.New("mcp effective config scope does not match its source config")
	}
	if _, err := uuid.Parse(cfg.ID); err != nil {
		return Registration{}, errors.New("mcp plugin config id is not a UUID")
	}
	if len(cfg.MCPServers) != 0 {
		if len(cfg.MCPServers) != 1 {
			return Registration{}, errors.New("mcp package has multiple servers; resolve a child registration")
		}
		return RegistrationFromPluginChild(def, cfg, effective, cfg.MCPServers[0], observation, authority)
	}

	payload, err := decodeMCPPluginPayloadSingle(effective.Payload)
	if err != nil {
		return Registration{}, err
	}
	credentialRef, credentialMode, oauthClientSecretRef, err := decodeMCPPluginCredentialRefsForKey(cfg.CredentialRefs, cfg, "main", payload.AuthType, payload.CredentialMode)
	if err != nil {
		return Registration{}, err
	}
	if oauthClientSecretRef != "" && metadataOAuthClientID(payload.Metadata) == "" {
		return Registration{}, errors.New("MCP OAuth client secret requires a client id")
	}
	status, statusError, observedAt, tools, err := safeMCPObservation(observation, cfg, credentialMode, authority)
	if err != nil {
		return Registration{}, err
	}
	return Registration{
		ID:                   cfg.ID,
		ParentConfigID:       cfg.ID,
		ServerKey:            "main",
		PluginID:             def.ID,
		ConfigRevision:       cfg.Revision,
		Scope:                string(cfg.Scope),
		UserID:               cfg.UserID,
		AgentID:              cfg.AgentID,
		Name:                 def.DisplayName,
		URL:                  payload.URL,
		Transport:            payload.Transport,
		AuthType:             payload.AuthType,
		CredentialRef:        credentialRef,
		Enabled:              effective.IsEffectivelyEnabled,
		Status:               status,
		StatusError:          statusError,
		ProbedAt:             observedAt,
		Tools:                tools,
		CredentialMode:       credentialMode,
		Headers:              cloneHeaders(payload.Headers),
		Metadata:             payload.Metadata,
		Description:          payload.Description,
		OAuthClientID:        metadataOAuthClientID(payload.Metadata),
		OAuthClientSecretRef: oauthClientSecretRef,
		CreatedAt:            cfg.CreatedAt.UTC(),
		UpdatedAt:            cfg.UpdatedAt.UTC(),
	}, nil
}

// decodeMCPPluginPayloadSingle reads the formal named-map shape when exactly
// one authored server is expected. The parent envelope is never interpreted
// as server metadata.
func decodeMCPPluginPayloadSingle(raw json.RawMessage) (mcpPluginPayload, error) {
	payloads, err := decodeMCPPluginPayloads(raw)
	if err != nil {
		return mcpPluginPayload{}, err
	}
	if len(payloads) != 1 {
		return mcpPluginPayload{}, errors.New("MCP package has multiple servers; select a child")
	}
	for _, payload := range payloads {
		return payload, nil
	}
	return mcpPluginPayload{}, errors.New("MCP package has no servers")
}

// RegistrationFromPluginChild adapts one child entry from a composable MCP
// package. The parent config remains the only revision authority; the child
// UUID is the runtime, Vault, OAuth and observation identity.
func RegistrationFromPluginChild(def plugin.Definition, cfg plugin.Config, effective plugin.Effective, child plugin.MCPServerChild, observation PluginMCPObservation, authority authz.Authority) (Registration, error) {
	if !authority.Valid() {
		return Registration{}, authz.ErrForbidden
	}
	if err := def.Validate(); err != nil {
		return Registration{}, fmt.Errorf("mcp plugin definition: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return Registration{}, fmt.Errorf("mcp plugin config: %w", err)
	}
	if cfg.PluginID != def.ID || effective.PluginID != def.ID || effective.ConfigID != cfg.ID {
		return Registration{}, errors.New("mcp child config does not match its parent definition")
	}
	if effective.SourceScope != cfg.Scope || child.ParentConfigID != cfg.ID || child.ID == "" || child.ServerKey == "" {
		return Registration{}, errors.New("mcp child identity does not match its parent config")
	}
	if _, err := uuid.Parse(child.ID); err != nil {
		return Registration{}, errors.New("mcp child id is not a UUID")
	}
	if _, err := uuid.Parse(cfg.ID); err != nil {
		return Registration{}, errors.New("mcp parent config id is not a UUID")
	}
	payload, err := decodeMCPPluginPayloadForKey(effective.Payload, child.ServerKey)
	if err != nil {
		return Registration{}, err
	}
	childCfg := cfg
	childCfg.ID = child.ID
	credentialRef, credentialMode, oauthClientSecretRef, err := decodeMCPPluginCredentialRefsForKey(cfg.CredentialRefs, childCfg, child.ServerKey, payload.AuthType, payload.CredentialMode)
	if err != nil {
		return Registration{}, err
	}
	if oauthClientSecretRef != "" && metadataOAuthClientID(payload.Metadata) == "" {
		return Registration{}, errors.New("MCP OAuth client secret requires a client id")
	}
	status, statusError, observedAt, tools, err := safeMCPObservation(observation, childCfg, credentialMode, authority)
	if err != nil {
		return Registration{}, err
	}
	createdAt, updatedAt := cfg.CreatedAt.UTC(), cfg.UpdatedAt.UTC()
	if !child.CreatedAt.IsZero() {
		createdAt = child.CreatedAt.UTC()
	}
	if !child.UpdatedAt.IsZero() {
		updatedAt = child.UpdatedAt.UTC()
	}
	return Registration{
		ID: child.ID, ParentConfigID: cfg.ID, ServerKey: child.ServerKey,
		PluginID: def.ID, ConfigRevision: cfg.Revision,
		Scope: string(cfg.Scope), UserID: cfg.UserID, AgentID: cfg.AgentID,
		Name: def.DisplayName, URL: payload.URL, Transport: payload.Transport,
		AuthType: payload.AuthType, CredentialRef: credentialRef,
		Enabled: effective.IsEffectivelyEnabled, Status: status, StatusError: statusError,
		ProbedAt: observedAt, Tools: tools, CredentialMode: credentialMode,
		Headers: cloneHeaders(payload.Headers), Metadata: payload.Metadata, Description: payload.Description,
		OAuthClientID: metadataOAuthClientID(payload.Metadata), OAuthClientSecretRef: oauthClientSecretRef,
		CreatedAt: createdAt, UpdatedAt: updatedAt,
	}, nil
}

type mcpPluginPayload struct {
	URL            string
	Transport      string
	AuthType       string
	CredentialMode string
	Headers        map[string]string
	Description    string
	Metadata       map[string]any
}

// decodeMCPPluginPayloadForKey selects one authored child from the parent's
// mcp_servers map. The parent map is configuration data; stable child UUIDs
// come from plugin.Config.MCPServers and are never synthesized here.
func decodeMCPPluginPayloadForKey(raw json.RawMessage, key string) (mcpPluginPayload, error) {
	if strings.TrimSpace(key) == "" {
		return mcpPluginPayload{}, errors.New("MCP server key is required")
	}
	object, err := decodeJSONObject(raw, "MCP config payload")
	if err != nil {
		return mcpPluginPayload{}, err
	}
	serversRaw, ok := object["mcp_servers"]
	if !ok {
		return mcpPluginPayload{}, errors.New("MCP config payload is missing mcp_servers")
	}
	servers, err := decodeJSONObject(serversRaw, "MCP mcp_servers payload")
	if err != nil {
		return mcpPluginPayload{}, err
	}
	childRaw, ok := servers[key]
	if !ok {
		return mcpPluginPayload{}, fmt.Errorf("MCP config payload has no server %q", key)
	}
	return decodeMCPPluginChildPayload(childRaw)
}

// decodeMCPPluginPayloads returns authored server payloads keyed by package
// key. Only the formal mcp_servers named map is accepted.
func decodeMCPPluginPayloads(raw json.RawMessage) (map[string]mcpPluginPayload, error) {
	object, err := decodeJSONObject(raw, "MCP config payload")
	if err != nil {
		return nil, err
	}
	if nested, ok := object["mcp_servers"]; ok {
		servers, err := decodeJSONObject(nested, "MCP mcp_servers payload")
		if err != nil {
			return nil, err
		}
		result := make(map[string]mcpPluginPayload, len(servers))
		for key, child := range servers {
			if strings.TrimSpace(key) == "" {
				return nil, errors.New("MCP server key must not be empty")
			}
			payload, err := decodeMCPPluginChildPayload(child)
			if err != nil {
				return nil, fmt.Errorf("MCP server %q: %w", key, err)
			}
			result[key] = payload
		}
		return result, nil
	}
	if _, err := plugin.DecodeResourcePayload(raw, "MCP config payload"); err != nil {
		return nil, err
	}
	return nil, nil
}

// decodeMCPPluginObservationPayload gives the observation reader a credential
// mode only when it is unambiguous. A parent with several children has no
// parent-level observation row that can safely be applied to every child.
func decodeMCPPluginObservationPayload(raw json.RawMessage) (mcpPluginPayload, bool, error) {
	payloads, err := decodeMCPPluginPayloads(raw)
	if err != nil {
		return mcpPluginPayload{}, false, err
	}
	if len(payloads) != 1 {
		return mcpPluginPayload{}, false, nil
	}
	for _, payload := range payloads {
		return payload, true, nil
	}
	return mcpPluginPayload{}, false, nil
}

func decodeMCPPluginChildPayload(raw json.RawMessage) (mcpPluginPayload, error) {
	object, err := decodeJSONObject(raw, "MCP server payload")
	if err != nil {
		return mcpPluginPayload{}, err
	}
	for key := range object {
		switch key {
		case "url", "transport", "auth_type", "credential_mode", "headers", "metadata", "description":
		default:
			return mcpPluginPayload{}, fmt.Errorf("mcp server payload contains unsupported field %q", key)
		}
	}
	payload := mcpPluginPayload{CredentialMode: CredentialModeShared, Metadata: map[string]any{}}
	if payload.URL, err = requiredJSONString(object, "url"); err != nil {
		return mcpPluginPayload{}, err
	}
	if payload.Transport, err = requiredJSONString(object, "transport"); err != nil {
		return mcpPluginPayload{}, err
	}
	if !ValidTransport(payload.Transport) {
		return mcpPluginPayload{}, errors.New("mcp server payload has unsupported transport")
	}
	if payload.AuthType, err = requiredJSONString(object, "auth_type"); err != nil {
		return mcpPluginPayload{}, err
	}
	if !ValidAuthType(payload.AuthType) {
		return mcpPluginPayload{}, errors.New("mcp server payload has unsupported auth type")
	}
	if value, ok := object["credential_mode"]; ok {
		if isJSONNull(value) || json.Unmarshal(value, &payload.CredentialMode) != nil || !ValidCredentialMode(payload.CredentialMode) {
			return mcpPluginPayload{}, errors.New("mcp server payload has unsupported credential mode")
		}
	}
	if payload.CredentialMode == CredentialModePerUser && payload.AuthType != AuthTypeOAuth {
		return mcpPluginPayload{}, errors.New("mcp server payload has per-user credentials without OAuth")
	}
	if err := decodePublicHeaders(object, &payload.Headers); err != nil {
		return mcpPluginPayload{}, err
	}
	if value, ok := object["metadata"]; ok {
		payload.Metadata, err = decodeMCPPluginMetadata(value)
		if err != nil {
			return mcpPluginPayload{}, err
		}
	}
	if value, ok := object["description"]; ok {
		if isJSONNull(value) || json.Unmarshal(value, &payload.Description) != nil {
			return mcpPluginPayload{}, errors.New("MCP server payload description must be a string")
		}
	}
	return payload, nil
}

// NewMCPPayloadValidator returns the plugin service validator for MCP configs.
// Endpoint policy is applied even to disabled payload-bearing configs because
// disabled is an availability decision, not permission to persist unsafe URLs.
func NewMCPPayloadValidator(policy EndpointPolicy) plugin.PayloadValidator {
	return func(ctx context.Context, definition plugin.Definition, config plugin.Config, resetFields []string) error {
		return ValidateMCPPayload(ctx, policy, definition, config, resetFields)
	}
}

// ValidateMCPPayload validates resolved MCP data without dialing its endpoint.
// A negative config has no backend payload; its empty credential refs are still
// checked by Config.Validate. A disabled payload is fully safety-validated.
func ValidateMCPPayload(_ context.Context, policy EndpointPolicy, definition plugin.Definition, config plugin.Config, resetFields []string) error {
	if err := definition.Validate(); err != nil {
		return fmt.Errorf("MCP definition: %w", err)
	}
	if err := config.Validate(); err != nil {
		return fmt.Errorf("MCP config: %w", err)
	}
	if config.PluginID != definition.ID {
		return errors.New("MCP config does not match its definition")
	}
	if !definitionHasMCP(definition) && !payloadHasMCP(config.Payload) {
		return nil
	}
	if err := validateMCPDefinitionSpec(definition.Spec); err != nil {
		return err
	}
	if len(config.Payload) == 0 {
		// Config.Validate has already enforced disabled + empty refs for a
		// negative record. There is no endpoint or auth payload to inspect.
		return nil
	}
	payloads, err := decodeMCPPluginPayloads(config.Payload)
	if err != nil {
		return err
	}
	nestedPayload := payloadHasMCP(config.Payload)
	for key, payload := range payloads {
		if err := policy.validateEndpointURL(payload.URL); err != nil {
			return errors.New("MCP config endpoint is not allowed by endpoint policy")
		}
		childCfg := config
		refsDecoder := decodeMCPPluginCredentialRefs
		if nestedPayload {
			for _, child := range config.MCPServers {
				if child.ServerKey == key {
					childCfg.ID = child.ID
					break
				}
			}
			refsDecoder = func(raw json.RawMessage, cfg plugin.Config, authType, mode string) (string, string, string, error) {
				return decodeMCPPluginCredentialRefsForKey(raw, cfg, key, authType, mode)
			}
		}
		_, _, oauthClientSecretRef, err := refsDecoder(config.CredentialRefs, childCfg, payload.AuthType, payload.CredentialMode)
		if err != nil {
			return err
		}
		if oauthClientSecretRef != "" && metadataOAuthClientID(payload.Metadata) == "" {
			return errors.New("MCP OAuth client secret requires a client id")
		}
	}
	return nil
}

func definitionHasMCP(def plugin.Definition) bool { return payloadHasMCP(def.Spec) }
func payloadHasMCP(raw json.RawMessage) bool {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || object == nil {
		return false
	}
	if _, ok := object["mcp_servers"]; ok {
		return true
	}
	return false
}

func validateMCPDefinitionSpec(raw json.RawMessage) error {
	payload, err := plugin.DecodeResourcePayload(raw, "MCP definition spec")
	if err != nil {
		return err
	}
	for key, server := range payload.MCPServers {
		if strings.TrimSpace(key) == "" {
			return errors.New("MCP definition server key must not be empty")
		}
		// A definition can declare a connection whose endpoint and auth are
		// supplied by its config. Validate completeness on the resolved payload.
		if server.Transport != "" && !ValidTransport(server.Transport) {
			return errors.New("MCP definition has unsupported transport")
		}
		if server.AuthType != "" && !ValidAuthType(server.AuthType) {
			return errors.New("MCP definition has unsupported auth type")
		}
		if server.CredentialMode != "" && !ValidCredentialMode(server.CredentialMode) {
			return errors.New("MCP definition has unsupported credential mode")
		}
	}
	return nil
}

func mergeMCPJSONObjects(definition, config json.RawMessage) (json.RawMessage, error) {
	return plugin.MergeDefinitionConfig(definition, config)
}

func cloneHeaders(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	maps.Copy(out, in)
	return out
}

func decodePublicHeaders(object map[string]json.RawMessage, dst *map[string]string) error {
	raw, ok := object["headers"]
	if !ok {
		return nil
	}
	if isJSONNull(raw) || json.Unmarshal(raw, dst) != nil || !validPublicHeaders(*dst) {
		return errors.New("MCP server payload headers must be a valid public header map")
	}
	return nil
}

func validPublicHeaders(headers map[string]string) bool {
	seen := make(map[string]struct{}, len(headers))
	for name, value := range headers {
		canonical := strings.ToLower(name)
		if _, exists := seen[canonical]; exists || !validHeaderName(name) || !validHeaderValue(value) {
			return false
		}
		switch canonical {
		case "authorization", "proxy-authorization", "cookie", "set-cookie", "x-api-key", "api-key":
			return false
		}
		seen[canonical] = struct{}{}
	}
	return true
}

func validHeaderName(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r <= 0x20 || r >= 0x7f || strings.ContainsRune(`()<>@,;:\"/[]?={} \t`, r) {
			return false
		}
	}
	return true
}

func validHeaderValue(value string) bool {
	return !strings.ContainsAny(value, "\r\n") && !strings.ContainsRune(value, 0)
}

func decodeMCPPluginCredentialRefs(raw json.RawMessage, cfg plugin.Config, authType, mode string) (string, string, string, error) {
	object, err := decodeJSONObject(raw, "MCP credential refs")
	if err != nil {
		return "", "", "", err
	}
	for key := range object {
		switch key {
		case "bearer", "oauth_bundle", "oauth_client_secret":
		default:
			return "", "", "", fmt.Errorf("MCP credential refs contain unsupported field %q", key)
		}
	}
	if authType == AuthTypeNone && len(object) != 0 {
		return "", "", "", errors.New("MCP no-auth config contains credential refs")
	}
	if authType == AuthTypeBearer {
		ref, ok := object["bearer"]
		if !ok {
			return "", "", "", errors.New("MCP bearer config is missing its credential locator")
		}
		locator, err := decodeLocator(ref, "bearer")
		if err != nil {
			return "", "", "", err
		}
		if locator.Name != credentialName(cfg.ID) {
			return "", "", "", errors.New("MCP bearer locator does not match config identity")
		}
		if err := validateLocatorOwner(locator, cfg); err != nil {
			return "", "", "", fmt.Errorf("MCP bearer locator: %w", err)
		}
		if len(object) != 1 {
			return "", "", "", errors.New("MCP bearer config contains unrelated credential refs")
		}
		return locator.Name, CredentialModeShared, "", nil
	}
	if authType != AuthTypeOAuth {
		if len(object) != 0 {
			return "", "", "", errors.New("MCP config contains credential refs for an unsupported auth type")
		}
		return "", CredentialModeShared, "", nil
	}
	bundleRaw, ok := object["oauth_bundle"]
	if !ok {
		return "", "", "", errors.New("MCP OAuth config is missing its bundle locator")
	}
	bundle, err := decodeLocator(bundleRaw, "oauth_bundle")
	if err != nil {
		return "", "", "", err
	}
	if bundle.Name != oauthBundleName(cfg.ID) {
		return "", "", "", errors.New("MCP OAuth bundle locator does not match config identity")
	}
	if bundle.Mode == "" {
		bundle.Mode = mode
	}
	if bundle.Mode != mode {
		return "", "", "", errors.New("MCP OAuth bundle mode does not match config payload")
	}
	if mode == CredentialModePerUser {
		if bundle.Owner != "per_user" || bundle.ScopeSet || bundle.UserIDSet || bundle.AgentIDSet {
			return "", "", "", errors.New("MCP per-user OAuth bundle has a registration owner")
		}
	} else if err := validateLocatorOwner(bundle, cfg); err != nil {
		return "", "", "", fmt.Errorf("MCP shared OAuth bundle: %w", err)
	}
	if len(object) > 2 {
		return "", "", "", errors.New("MCP OAuth config contains unsupported credential refs")
	}
	secretRef := ""
	if secret, exists := object["oauth_client_secret"]; exists {
		decoded, err := decodeLocator(secret, "oauth_client_secret")
		if err != nil {
			return "", "", "", err
		}
		if err := validateLocatorOwner(decoded, cfg); err != nil {
			return "", "", "", fmt.Errorf("MCP OAuth client secret: %w", err)
		}
		if decoded.Name != oauthClientSecretName(cfg.ID) {
			return "", "", "", errors.New("MCP OAuth client secret locator does not match config identity")
		}
		secretRef = decoded.Name
	}
	return "", mode, secretRef, nil
}

// decodeMCPPluginCredentialRefsForKey selects the credential locator family
// belonging to one child. The child UUID is supplied through cfg.ID, so every
// Vault locator remains independently namespaced while the parent JSON stays
// the sole authored credential-ref authority.
func decodeMCPPluginCredentialRefsForKey(raw json.RawMessage, cfg plugin.Config, serverKey, authType, mode string) (string, string, string, error) {
	// Callers that already selected a child pass its child-level refs object.
	// The parent-level path below always requires the formal mcp_servers map.
	if serverKey == "" {
		return decodeMCPPluginCredentialRefs(raw, cfg, authType, mode)
	}
	object, err := decodeJSONObject(raw, "MCP credential refs")
	if err != nil {
		return "", "", "", err
	}
	for key := range object {
		if key != "mcp_servers" && key != "session_env" {
			return "", "", "", errors.New("MCP credential refs must use the mcp_servers map")
		}
	}
	if nested, ok := object["mcp_servers"]; ok {
		servers, err := decodeJSONObject(nested, "MCP credential refs mcp_servers")
		if err != nil {
			return "", "", "", err
		}
		child, ok := servers[serverKey]
		if !ok {
			child = json.RawMessage(`{}`)
		}
		return decodeMCPPluginCredentialRefs(child, cfg, authType, mode)
	}
	return decodeMCPPluginCredentialRefs(json.RawMessage(`{}`), cfg, authType, mode)
}

type mcpCredentialLocator struct {
	Name                            string `json:"name"`
	Scope                           string `json:"scope,omitempty"`
	UserID                          string `json:"user_id,omitempty"`
	AgentID                         string `json:"agent_id,omitempty"`
	Mode                            string `json:"mode,omitempty"`
	Owner                           string `json:"owner,omitempty"`
	ScopeSet, UserIDSet, AgentIDSet bool
}

func decodeLocator(raw json.RawMessage, kind string) (mcpCredentialLocator, error) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return mcpCredentialLocator{}, fmt.Errorf("MCP %s locator must be an object", kind)
	}
	object, err := decodeJSONObject(raw, "MCP "+kind+" locator")
	if err != nil {
		return mcpCredentialLocator{}, err
	}
	for key, value := range object {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return mcpCredentialLocator{}, fmt.Errorf("MCP %s locator field %q must not be null", kind, key)
		}
	}
	var locator mcpCredentialLocator
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&locator); err != nil {
		return mcpCredentialLocator{}, fmt.Errorf("MCP %s locator has invalid fields", kind)
	}
	_, locator.ScopeSet = object["scope"]
	_, locator.UserIDSet = object["user_id"]
	_, locator.AgentIDSet = object["agent_id"]
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return mcpCredentialLocator{}, fmt.Errorf("MCP %s locator contains trailing data", kind)
	}
	if locator.Name == "" {
		return mcpCredentialLocator{}, fmt.Errorf("MCP %s locator has no name", kind)
	}
	return locator, nil
}

func validateLocatorOwner(locator mcpCredentialLocator, cfg plugin.Config) error {
	if !locator.ScopeSet || !locator.UserIDSet || !locator.AgentIDSet {
		return errors.New("locator owner tuple is incomplete")
	}
	if locator.Scope != string(cfg.Scope) || locator.UserID != cfg.UserID || locator.AgentID != cfg.AgentID {
		return errors.New("locator owner does not match config owner")
	}
	if locator.Owner != "" {
		return errors.New("registration-scoped locator has an unexpected per-user owner")
	}
	return nil
}

func decodeMCPPluginMetadata(raw json.RawMessage) (map[string]any, error) {
	object, err := decodeJSONObject(raw, "MCP metadata")
	if err != nil {
		return nil, err
	}
	for key := range object {
		switch key {
		case "call_timeout_seconds", "oauth", "registry":
		default:
			return nil, fmt.Errorf("MCP metadata contains unsupported field %q", key)
		}
	}
	metadata := make(map[string]any, len(object))
	if value, ok := object["call_timeout_seconds"]; ok {
		if isJSONNull(value) {
			return nil, errors.New("MCP call timeout metadata must not be null")
		}
		var seconds float64
		if err := json.Unmarshal(value, &seconds); err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) {
			return nil, errors.New("MCP call timeout metadata is not a finite number")
		}
		metadata["call_timeout_seconds"] = seconds
	}
	if value, ok := object["oauth"]; ok {
		oauth, err := decodeMCPMetadataObject(value, "oauth", map[string]bool{
			"client_id":                  true,
			"token_endpoint_auth_method": true,
		})
		if err != nil {
			return nil, err
		}
		if method, ok := oauth["token_endpoint_auth_method"].(string); ok {
			_, normalized, err := oauthTokenEndpointAuthStyle(method)
			if err != nil {
				return nil, fmt.Errorf("MCP OAuth metadata: %w", err)
			}
			oauth["token_endpoint_auth_method"] = normalized
		}
		metadata["oauth"] = oauth
	}
	if value, ok := object["registry"]; ok {
		registry, err := decodeMCPMetadataObject(value, "registry", map[string]bool{"source": true, "id": true, "version": true, "installed_at": true})
		if err != nil {
			return nil, err
		}
		metadata["registry"] = registry
	}
	return metadata, nil
}

func decodeMCPMetadataObject(raw json.RawMessage, name string, allowed map[string]bool) (map[string]any, error) {
	object, err := decodeJSONObject(raw, "MCP "+name+" metadata")
	if err != nil {
		return nil, err
	}
	out := make(map[string]any, len(object))
	for key, value := range object {
		if !allowed[key] {
			return nil, fmt.Errorf("MCP %s metadata contains unsupported field %q", name, key)
		}
		if isJSONNull(value) {
			return nil, fmt.Errorf("MCP %s metadata field %q must not be null", name, key)
		}
		var text string
		if err := json.Unmarshal(value, &text); err != nil {
			return nil, fmt.Errorf("MCP %s metadata field %q is not a string", name, key)
		}
		out[key] = text
	}
	return out, nil
}

func isJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func decodeJSONObject(raw json.RawMessage, label string) (map[string]json.RawMessage, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, fmt.Errorf("%s is missing", label)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var object map[string]json.RawMessage
	if err := decoder.Decode(&object); err != nil || object == nil {
		return nil, fmt.Errorf("%s must be an object", label)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%s contains trailing data", label)
	}
	return object, nil
}

func requiredJSONString(object map[string]json.RawMessage, key string) (string, error) {
	raw, ok := object[key]
	if !ok {
		return "", fmt.Errorf("MCP config payload is missing %q", key)
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil || strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("MCP config payload field %q is not a non-empty string", key)
	}
	return value, nil
}

func safeMCPObservation(observation PluginMCPObservation, cfg plugin.Config, credentialMode string, authority authz.Authority) (string, string, time.Time, []CatalogTool, error) {
	if observation.ConfigRevision != 0 && observation.ConfigRevision != cfg.Revision {
		return StatusUnknown, "", time.Time{}, nil, nil
	}
	if !observationOwnerMatches(observation.CredentialUserID, credentialMode, authority) {
		return StatusUnknown, "", time.Time{}, nil, nil
	}
	status := observation.Status
	if status == "" {
		status = StatusUnknown
	}
	if !ValidStatus(status) {
		return "", "", time.Time{}, nil, errors.New("MCP observation has an unsupported status")
	}
	statusError := ""
	switch status {
	case StatusNeedsAuth:
		if observation.StatusError != "" {
			statusError = credentialRejectedHint
		}
	case StatusError:
		if observation.StatusError != "" {
			statusError = "MCP probe failed"
		}
	}
	tools, err := cloneMCPObservationTools(observation.Tools)
	if err != nil {
		return "", "", time.Time{}, nil, err
	}
	return status, statusError, observation.ProbedAt.UTC(), tools, nil
}

func observationOwnerMatches(observedUserID, credentialMode string, authority authz.Authority) bool {
	if credentialMode == CredentialModeShared {
		return observedUserID == ""
	}
	if credentialMode != CredentialModePerUser {
		return false
	}
	if authority.Kind() != authz.ActorUser && authority.Kind() != authz.ActorAgent {
		return false
	}
	return observedUserID != "" && observedUserID == string(authority.UserID())
}

func cloneMCPObservationTools(in []CatalogTool) ([]CatalogTool, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make([]CatalogTool, len(in))
	for i, tool := range in {
		if strings.TrimSpace(tool.Name) == "" {
			return nil, errors.New("MCP observation contains a tool without a remote name")
		}
		out[i] = CatalogTool{Name: tool.Name, Description: tool.Description}
		out[i].InputSchema = cloneSchema(tool.InputSchema)
		out[i].Annotations = cloneSchema(tool.Annotations)
	}
	return out, nil
}

func metadataOAuthClientID(metadata map[string]any) string {
	oauth, _ := metadata["oauth"].(map[string]any)
	clientID, _ := oauth["client_id"].(string)
	return clientID
}
