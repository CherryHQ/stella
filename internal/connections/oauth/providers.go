package oauth

import (
	"errors"
	"fmt"

	"golang.org/x/oauth2"
	"gopkg.in/yaml.v3"
)

// ProviderFlowDefinition is the authored configuration for one OAuth flow.
// AuthStyle uses the YAML vocabulary so parsing does not expose an OAuth
// implementation detail to the resource format.
type ProviderFlowDefinition struct {
	Type          string `json:"type" yaml:"type"`
	AuthURL       string `json:"auth_url,omitempty" yaml:"auth_url,omitempty"`
	DeviceAuthURL string `json:"device_auth_url,omitempty" yaml:"device_auth_url,omitempty"`
	TokenURL      string `json:"token_url" yaml:"token_url"`
	AuthStyle     string `json:"auth_style,omitempty" yaml:"auth_style,omitempty"`
	PKCE          bool   `json:"pkce,omitempty" yaml:"pkce,omitempty"`
}

// ProviderDefinition is the authored static configuration for one OAuth
// provider. ClientID and ClientSecret are defaults; database overrides take
// precedence when a flow starts.
type ProviderDefinition struct {
	ID           string                   `json:"id" yaml:"id"`
	Icon         string                   `json:"icon,omitempty" yaml:"icon,omitempty"`
	Scopes       []string                 `json:"scopes" yaml:"scopes"`
	VaultKey     string                   `json:"vault_key" yaml:"vault_key"`
	Flows        []ProviderFlowDefinition `json:"flows" yaml:"flows"`
	ClientID     string                   `json:"client_id,omitempty" yaml:"client_id,omitempty"`
	ClientSecret string                   `json:"client_secret,omitempty" yaml:"client_secret,omitempty"`
}

type providerDocument struct {
	OAuthProviders []ProviderDefinition `yaml:"oauth_providers,omitempty"`
}

// ParseProviders decodes and validates an OAuth provider YAML document.
// Provider validation intentionally matches the release manifest contract:
// duplicate IDs and incomplete flow endpoints are rejected, while unknown or
// omitted auth_style values retain oauth2's auto-detection behavior.
func ParseProviders(data []byte) ([]ProviderDefinition, error) {
	var document providerDocument
	if err := yaml.Unmarshal(data, &document); err != nil {
		return nil, err
	}
	providers := document.OAuthProviders
	if err := validateProviders(providers); err != nil {
		return nil, err
	}
	return providers, nil
}

// ProviderIDs returns the provider identities in an OAuth YAML document.
// ParseProviders also validates each definition, so callers cannot build a
// cross-file reference index from malformed provider data.
func ProviderIDs(data []byte) ([]string, error) {
	providers, err := ParseProviders(data)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(providers))
	for _, provider := range providers {
		ids = append(ids, provider.ID)
	}
	return ids, nil
}

// NewBuiltinRegistry parses OAuth resource data and builds the runtime
// registry. Keeping the YAML-to-oauth2 conversion here prevents callers from
// depending on the authored provider representation.
func NewBuiltinRegistry(data []byte) (*ProviderRegistry, error) {
	providers, err := ParseProviders(data)
	if err != nil {
		return nil, err
	}
	registry := NewProviderRegistry()
	for _, provider := range providers {
		registry.Register(providerConfig(provider))
	}
	return registry, nil
}

func providerConfig(provider ProviderDefinition) ProviderConfig {
	flows := make([]ProviderFlowConfig, 0, len(provider.Flows))
	for _, flow := range provider.Flows {
		flows = append(flows, ProviderFlowConfig{
			Type:          flow.Type,
			AuthURL:       flow.AuthURL,
			DeviceAuthURL: flow.DeviceAuthURL,
			TokenURL:      flow.TokenURL,
			AuthStyle:     authStyle(flow.AuthStyle),
			PKCE:          flow.PKCE,
		})
	}
	return ProviderConfig{
		ID:           provider.ID,
		Icon:         provider.Icon,
		Scopes:       provider.Scopes,
		VaultKey:     provider.VaultKey,
		Flows:        flows,
		ClientID:     provider.ClientID,
		ClientSecret: provider.ClientSecret,
	}
}

func authStyle(value string) oauth2.AuthStyle {
	switch value {
	case "in_params":
		return oauth2.AuthStyleInParams
	case "in_header":
		return oauth2.AuthStyleInHeader
	default:
		return oauth2.AuthStyleAutoDetect
	}
}

func validateProviders(providers []ProviderDefinition) error {
	var errs []error
	providerIDs := make(map[string]struct{}, len(providers))
	for i, provider := range providers {
		if provider.ID == "" {
			errs = append(errs, fmt.Errorf("oauth_provider[%d]: id is required", i))
		} else {
			if _, duplicate := providerIDs[provider.ID]; duplicate {
				errs = append(errs, fmt.Errorf("oauth_provider[%d]: duplicate id %q", i, provider.ID))
			}
			providerIDs[provider.ID] = struct{}{}
		}
		if provider.VaultKey == "" {
			errs = append(errs, fmt.Errorf("oauth_provider %q: vault_key is required", provider.ID))
		}
		if len(provider.Flows) == 0 {
			errs = append(errs, fmt.Errorf("oauth_provider %q: at least one flow is required", provider.ID))
		}
		for j, flow := range provider.Flows {
			if flow.Type == "" {
				errs = append(errs, fmt.Errorf("oauth_provider %q flow[%d]: type is required", provider.ID, j))
			} else if flow.Type != "authorization_code" && flow.Type != "device_code" {
				errs = append(errs, fmt.Errorf("oauth_provider %q flow[%d]: unknown type %q", provider.ID, j, flow.Type))
			}
			if flow.TokenURL == "" {
				errs = append(errs, fmt.Errorf("oauth_provider %q flow[%d]: token_url is required", provider.ID, j))
			}
			if flow.Type == "authorization_code" && flow.AuthURL == "" {
				errs = append(errs, fmt.Errorf("oauth_provider %q flow[%d]: auth_url is required for authorization_code", provider.ID, j))
			}
			if flow.Type == "device_code" && flow.DeviceAuthURL == "" {
				errs = append(errs, fmt.Errorf("oauth_provider %q flow[%d]: device_auth_url is required for device_code", provider.ID, j))
			}
		}
	}
	return errors.Join(errs...)
}
