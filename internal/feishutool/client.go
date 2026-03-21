package feishutool

import (
	"context"
	"fmt"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	"golang.org/x/time/rate"
)

const defaultRateLimit = 50 // requests per second

// Client wraps a Lark SDK client with rate limiting.
// Phase 2 will add UAT (user access token) support.
type Client struct {
	lark    *lark.Client
	limiter *rate.Limiter
}

// ClientOption configures the Client.
type ClientOption func(*Client)

// WithRateLimit sets the per-second rate limit for API calls.
func WithRateLimit(rps int) ClientOption {
	return func(c *Client) {
		c.limiter = rate.NewLimiter(rate.Limit(rps), rps)
	}
}

// NewClient creates a feishutool Client wrapping the given Lark SDK client.
func NewClient(larkClient *lark.Client, opts ...ClientOption) *Client {
	c := &Client{
		lark:    larkClient,
		limiter: rate.NewLimiter(rate.Limit(defaultRateLimit), defaultRateLimit),
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Lark returns the underlying Lark SDK client for direct API access.
func (c *Client) Lark() *lark.Client {
	return c.lark
}

// Wait blocks until the rate limiter allows one request.
// Returns an error if the context is cancelled while waiting.
func (c *Client) Wait(ctx context.Context) error {
	if err := c.limiter.Wait(ctx); err != nil {
		return fmt.Errorf("feishutool: rate limit wait: %w", err)
	}
	return nil
}
