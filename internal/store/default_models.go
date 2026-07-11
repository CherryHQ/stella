package store

import "github.com/CherryHQ/stella/internal/config"

const (
	defaultAnthropicModelID      = "claude-sonnet-4-6"
	defaultOpenAIModelID         = "gpt-4o"
	defaultOpenAIResponseModelID = "gpt-4o"
)

// defaultModelIDForProviderType returns the preferred starter model ID for a
// built-in provider type.
func defaultModelIDForProviderType(providerType string) string {
	switch providerType {
	case "anthropic":
		return defaultAnthropicModelID
	case "openai":
		return defaultOpenAIModelID
	case "openai-response":
		return defaultOpenAIResponseModelID
	default:
		return ""
	}
}

// defaultModelRefForProviderType returns the default provider/model ref for the
// given provider type using the provider type as the prefix.
func defaultModelRefForProviderType(providerType string) string {
	modelID := defaultModelIDForProviderType(providerType)
	if providerType == "" || modelID == "" {
		return ""
	}
	return providerType + "/" + modelID
}

// defaultModelRefForProvider returns the default provider/model ref for a
// concrete provider instance.
func defaultModelRefForProvider(provider config.Provider) string {
	providerType := provider.Type
	if providerType == "" {
		providerType = provider.ID
	}
	modelID := defaultModelIDForProviderType(providerType)
	if providerType == "" || modelID == "" {
		return ""
	}
	return providerType + "/" + modelID
}

// defaultAgentModelRef picks the preferred starter model ref from the
// configured providers. It prefers Anthropic, then OpenAI, then OpenAI
// Responses, and falls back to the Anthropic compatibility alias.
func defaultAgentModelRef(providers []config.Provider) string {
	preferredTypes := []string{"anthropic", "openai", "openai-response"}
	for _, providerType := range preferredTypes {
		for _, provider := range providers {
			candidateType := provider.Type
			if candidateType == "" {
				candidateType = provider.ID
			}
			if candidateType != providerType {
				continue
			}
			if ref := defaultModelRefForProvider(provider); ref != "" {
				return ref
			}
		}
	}
	return defaultModelRefForProviderType("anthropic")
}
