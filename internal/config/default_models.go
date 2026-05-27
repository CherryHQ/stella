package config

const (
	DefaultAnthropicModelID      = "claude-sonnet-4-6"
	DefaultOpenAIModelID         = "gpt-4o"
	DefaultOpenAIResponseModelID = "gpt-4o"
)

// DefaultModelIDForProviderType returns the preferred starter model ID for a
// built-in provider type.
func DefaultModelIDForProviderType(providerType string) string {
	switch providerType {
	case "anthropic":
		return DefaultAnthropicModelID
	case "openai":
		return DefaultOpenAIModelID
	case "openai-response":
		return DefaultOpenAIResponseModelID
	default:
		return ""
	}
}

// DefaultModelRefForProviderType returns the default provider/model ref for the
// given provider type using the provider type as the prefix.
func DefaultModelRefForProviderType(providerType string) string {
	modelID := DefaultModelIDForProviderType(providerType)
	if providerType == "" || modelID == "" {
		return ""
	}
	return providerType + "/" + modelID
}

// DefaultModelRefForProvider returns the default provider/model ref for a
// concrete provider instance.
func DefaultModelRefForProvider(provider Provider) string {
	providerType := provider.Type
	if providerType == "" {
		providerType = provider.ID
	}
	modelID := DefaultModelIDForProviderType(providerType)
	if providerType == "" || modelID == "" {
		return ""
	}
	return providerType + "/" + modelID
}

// DefaultAgentModelRef picks the preferred starter model ref from the
// configured providers. It prefers Anthropic, then OpenAI, then OpenAI
// Responses, and falls back to the Anthropic compatibility alias.
func DefaultAgentModelRef(providers []Provider) string {
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
			if ref := DefaultModelRefForProvider(provider); ref != "" {
				return ref
			}
		}
	}
	return DefaultModelRefForProviderType("anthropic")
}
