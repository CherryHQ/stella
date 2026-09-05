package feishu

import (
	"context"
	"testing"
)

func TestOperationContextSurvivesBotStop(t *testing.T) {
	b, err := New(Config{AppID: "app", AppSecret: "secret"}, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	b.ctx, b.cancel = context.WithCancel(context.Background())

	opCtx, cancel := b.operationContext()
	defer cancel()

	b.Stop()

	select {
	case <-opCtx.Done():
		t.Fatal("operation context should not be cancelled when bot runtime stops")
	default:
	}
}
