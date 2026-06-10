package ai

import "time"

// StreamOptions configures provider-level streaming behavior.
type StreamOptions struct {
	Temperature     *float64
	MaxTokens       *int
	Reasoning       ThinkingLevel
	ThinkingBudgets *ThinkingBudgets
	Transport       Transport
	CacheRetention  CacheRetention
	SessionID       string
	Headers         map[string]string
	Metadata        map[string]any
	MaxRetryDelayMS *int
	Timeout         time.Duration
}

// CompleteOptions configures non-streaming requests.
type CompleteOptions struct {
	StreamOptions
}
