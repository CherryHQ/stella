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

// ModelCost describes per-million-token pricing.
type ModelCost struct {
	Input      float64
	Output     float64
	CacheRead  float64
	CacheWrite float64
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
	// never implicitly unsupported.
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
	CacheRead  float64
	CacheWrite float64
	Total      float64
}

// Usage tracks token accounting returned by providers.
type Usage struct {
	InputTokens  int
	OutputTokens int
	CacheRead    int
	CacheWrite   int
	TotalTokens  int
	Cost         UsageCost
}

// StopReason normalizes provider-specific stop signals.
type StopReason string

const (
	StopReasonStop    StopReason = "stop"
	StopReasonLength  StopReason = "length"
	StopReasonToolUse StopReason = "toolUse"
	StopReasonError   StopReason = "error"
	StopReasonAborted StopReason = "aborted"
)
