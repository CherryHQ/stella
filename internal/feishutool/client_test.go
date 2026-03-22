package feishutool

import (
	"context"
	"testing"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
)

func TestNewClient(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	c := NewClient(larkClient)

	if c.Lark() != larkClient {
		t.Fatal("Lark() should return the same client")
	}
}

func TestClientWait(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	c := NewClient(larkClient, WithRateLimit(100))

	ctx := context.Background()
	if err := c.Wait(ctx); err != nil {
		t.Fatalf("Wait should succeed: %v", err)
	}
}

func TestClientWaitCancelled(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	// Very low rate limit to force waiting.
	c := NewClient(larkClient, WithRateLimit(1))

	// Exhaust the burst.
	ctx := context.Background()
	_ = c.Wait(ctx)

	// Now cancel context — next wait should fail.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	// This should eventually fail due to context deadline.
	err := c.Wait(ctx)
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
}

func TestWithRateLimit(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	c := NewClient(larkClient, WithRateLimit(42))

	// Verify the limiter was configured (indirectly via successful wait).
	ctx := context.Background()
	if err := c.Wait(ctx); err != nil {
		t.Fatalf("Wait should succeed with custom rate limit: %v", err)
	}
}
