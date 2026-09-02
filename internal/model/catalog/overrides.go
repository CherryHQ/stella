package catalog

// ProviderOverride describes Stella-specific interpretation of a catalog entry.
type ProviderOverride struct {
	APIType     string
	BaseURL     string
	Unsupported bool
}

var providerOverrides = map[string]ProviderOverride{
	"openai":                   {APIType: "openai-response", BaseURL: "https://api.openai.com/v1"},
	"anthropic":                {APIType: "anthropic", BaseURL: "https://api.anthropic.com"},
	"xai":                      {APIType: "openai", BaseURL: "https://api.x.ai/v1"},
	"perplexity":               {APIType: "openai", BaseURL: "https://api.perplexity.ai"},
	"togetherai":               {APIType: "openai", BaseURL: "https://api.together.xyz/v1"},
	"groq":                     {APIType: "openai", BaseURL: "https://api.groq.com/openai/v1"},
	"mistral":                  {APIType: "openai", BaseURL: "https://api.mistral.ai/v1"},
	"cerebras":                 {APIType: "openai", BaseURL: "https://api.cerebras.ai/v1"},
	"aihubmix":                 {APIType: "openai", BaseURL: "https://aihubmix.com/v1"},
	"deepinfra":                {APIType: "openai", BaseURL: "https://api.deepinfra.com/v1/openai"},
	"google":                   {Unsupported: true},
	"google-vertex":            {Unsupported: true},
	"google-vertex-anthropic":  {Unsupported: true},
	"cohere":                   {Unsupported: true},
	"amazon-bedrock":           {Unsupported: true},
	"azure":                    {Unsupported: true},
	"azure-cognitive-services": {Unsupported: true},
}

// Override returns the explicit mapping, if one exists.
func Override(providerID string) (ProviderOverride, bool) {
	value, ok := providerOverrides[providerID]
	return value, ok
}

// APIType maps models.dev's SDK hint to one of Stella's installed adapters.
func APIType(providerID string, provider Provider) string {
	if override, ok := Override(providerID); ok && override.APIType != "" {
		return override.APIType
	}
	switch provider.NPM {
	case "@ai-sdk/anthropic":
		return "anthropic"
	case "@ai-sdk/openai":
		return "openai-response"
	default:
		return "openai"
	}
}

// BaseURL returns the catalog URL or Stella's maintained default for providers
// whose models.dev entry relies on an SDK-internal default.
func BaseURL(providerID string, provider Provider) string {
	if override, ok := Override(providerID); ok && override.BaseURL != "" {
		return override.BaseURL
	}
	return provider.API
}

func IsUnsupported(providerID string) bool {
	override, ok := Override(providerID)
	return ok && override.Unsupported
}
