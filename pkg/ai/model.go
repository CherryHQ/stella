package ai

import "slices"

// ToolDefinition describes a callable tool exposed to a model.
type ToolDefinition struct {
	Name        string
	Description string
	InputSchema map[string]any
}

// Api identifies the wire protocol family.
type Api = string

// Common API constants.
const (
	ApiOpenAICompletions     Api = "openai-completions"
	ApiOpenAIResponses       Api = "openai-responses"
	ApiAnthropicMessages     Api = "anthropic-messages"
	ApiBedrockConverseStream Api = "bedrock-converse-stream"
	ApiGoogleGenerativeAI    Api = "google-generative-ai"
	ApiGoogleVertex          Api = "google-vertex"
)

// Provider identifies the upstream service.
type Provider = string

// ThinkingLevel controls provider reasoning depth.
type ThinkingLevel = string

const (
	ThinkingMinimal ThinkingLevel = "minimal"
	ThinkingLow     ThinkingLevel = "low"
	ThinkingMedium  ThinkingLevel = "medium"
	ThinkingHigh    ThinkingLevel = "high"
	ThinkingXHigh   ThinkingLevel = "xhigh"
)

// ThinkingBudgets maps thinking levels to token budgets (token-based providers only).
type ThinkingBudgets struct {
	Minimal *int
	Low     *int
	Medium  *int
	High    *int
}

// CacheRetention controls prompt cache retention preference.
type CacheRetention = string

const (
	CacheNone  CacheRetention = "none"
	CacheShort CacheRetention = "short"
	CacheLong  CacheRetention = "long"
)

// Transport selects the streaming transport.
type Transport = string

const (
	TransportSSE       Transport = "sse"
	TransportWebSocket Transport = "websocket"
	TransportAuto      Transport = "auto"
)

// ModelRates is a set of per-million-token rates. Zero is a real free rate,
// never an inheritance sentinel at runtime.
type ModelRates struct {
	Input       float64
	Output      float64
	CacheRead   float64
	CacheWrite  float64
	Reasoning   float64
	InputAudio  float64
	OutputAudio float64
}

// ModelCostTier applies when the request context reaches MinContext.
type ModelCostTier struct {
	MinContext int
	ModelRates
}

// ModelCost is a base rate plus whole-request context tiers. Priced separates
// known-free models from models whose price is unknown.
type ModelCost struct {
	ModelRates
	Tiers  []ModelCostTier
	Priced bool
}

// Configured reports whether pricing data exists, including an explicit all-zero
// free price.
func (c ModelCost) Configured() bool { return c.Priced }

// RatesFor selects the highest tier whose threshold is met.
func (c ModelCost) RatesFor(promptTokens int) ModelRates {
	rates := c.ModelRates
	for _, tier := range c.Tiers {
		if tier.MinContext > promptTokens {
			break
		}
		rates = tier.ModelRates
	}
	return rates
}

// Model identifies a concrete model and its capabilities.
type Model struct {
	ID            string
	Name          string
	API           string
	Provider      string
	BaseURL       string
	Reasoning     bool
	Input         []string // e.g. ["text", "image"]
	Cost          ModelCost
	ContextWindow int
	MaxTokens     int
	Headers       map[string]string
}

// ImageCapability describes what the configuration says about a model's ability
// to accept image input. It is deliberately three-valued: "we know it cannot"
// and "we were never told" are different facts, and callers decide how to treat
// the latter.
type ImageCapability int

const (
	// ImageUnknown means no input modalities were declared, so the capability
	// was never stated. It is the zero value: an undeclared model is unknown,
	// never implicitly unsupported — callers that must choose treat it as
	// "cannot see", but the distinction stays visible here.
	ImageUnknown ImageCapability = iota
	// ImageSupported means the declared input modalities include images.
	ImageSupported
	// ImageUnsupported means input modalities were declared and images are not
	// among them.
	ImageUnsupported
)

// ImageCapability reports what the model's declared input modalities say about
// image input. Callers choose the policy for ImageUnknown.
func (m Model) ImageCapability() ImageCapability {
	switch {
	case len(m.Input) == 0:
		return ImageUnknown
	case slices.Contains(m.Input, "image"):
		return ImageSupported
	default:
		return ImageUnsupported
	}
}

// Context carries all conversation and tool state for a model request.
type Context struct {
	System   string
	Messages []Message
	Tools    []ToolDefinition
	Metadata map[string]string
}

// UsageCost tracks monetary cost per token category.
type UsageCost struct {
	Input      float64
	Output     float64
	Reasoning  float64
	CacheRead  float64
	CacheWrite float64
	Total      float64
}

// Usage tracks token accounting returned by providers.
// The token categories are disjoint: InputTokens counts only input that was not
// served from cache, so the four categories can be priced independently.
// Providers that fold cached tokens into their input count are normalized at
// their own boundary with UsageWithCachedInput. TotalTokens is the complete
// normalized total; consumers must not derive it by blindly summing the other
// fields.
type Usage struct {
	// Reported distinguishes a provider that sent an all-zero usage payload from
	// one that sent no usage payload at all. Do not infer this from token values.
	Reported        bool
	InputTokens     int
	OutputTokens    int
	ReasoningTokens int
	CacheRead       int
	CacheWrite      int
	TotalTokens     int
	Cost            UsageCost
	// CostConfigured records whether Cost was calculated from declared model
	// rates. A zero Cost without this bit means price is unknown, not free.
	CostConfigured bool
}

// UsageWithCachedInput builds a Usage from a provider that folds cache hits into
// its input count, which is what the OpenAI APIs do. Keeping the cached share in
// both InputTokens and CacheRead would bill it at the input rate and again at
// the cache-read rate; on a long session that is most of the reported cost.
func UsageWithCachedInput(input, output, cacheRead, total int) Usage {
	uncached := max(input-cacheRead,
		// A provider reporting more cache hits than input is malformed. Trust
		// the smaller, cheaper category rather than inventing negative usage.
		0)
	return Usage{
		InputTokens:  uncached,
		OutputTokens: output,
		CacheRead:    cacheRead,
		TotalTokens:  total,
	}
}

// PromptTokens is the actual context length. Input categories are disjoint,
// including Anthropic cache_creation (CacheWrite).
func (u Usage) PromptTokens() int { return u.InputTokens + u.CacheRead + u.CacheWrite }

// WithCost calculates the whole-request tier and splits completion reasoning
// from ordinary output. Audio rates are stored for display but not billed yet.
func (u Usage) WithCost(rates ModelCost) Usage {
	if !u.Reported || !rates.Configured() {
		return u
	}
	const perMillion = 1_000_000
	r := rates.RatesFor(u.PromptTokens())
	reasoningTokens := min(max(u.ReasoningTokens, 0), max(u.OutputTokens, 0))
	effectiveReasoningRate := r.Reasoning
	u.Cost = UsageCost{
		Input:      float64(u.InputTokens) * r.Input / perMillion,
		Output:     float64(u.OutputTokens-reasoningTokens) * r.Output / perMillion,
		Reasoning:  float64(reasoningTokens) * effectiveReasoningRate / perMillion,
		CacheRead:  float64(u.CacheRead) * r.CacheRead / perMillion,
		CacheWrite: float64(u.CacheWrite) * r.CacheWrite / perMillion,
	}
	u.Cost.Total = u.Cost.Input + u.Cost.Output + u.Cost.Reasoning + u.Cost.CacheRead + u.Cost.CacheWrite
	u.CostConfigured = true
	return u
}

// StopReason normalizes provider-specific stop signals.
type StopReason string

const (
	// StopReasonUnknown is an unrecognized or omitted provider finish reason.
	// Consumers that require a complete response must fail closed on it.
	StopReasonUnknown StopReason = ""
	StopReasonStop    StopReason = "stop"
	StopReasonLength  StopReason = "length"
	StopReasonToolUse StopReason = "toolUse"
	StopReasonError   StopReason = "error"
	StopReasonAborted StopReason = "aborted"
)
