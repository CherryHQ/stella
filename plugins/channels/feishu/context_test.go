package feishu

import (
	"context"
	"testing"
	"time"
)

func TestOperationContextSurvivesBotStop(t *testing.T) {
	b, err := New(Config{AppID: "app", AppSecret: "secret"}, nil)
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

func TestCancelStreamCancelsOperationContext(t *testing.T) {
	b, err := New(Config{AppID: "app", AppSecret: "secret"}, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	opCtx, cancel := b.operationContext()
	key := streamKey("oc_chat", "")
	b.registerStream(key, cancel)

	if !b.cancelStream(key) {
		t.Fatal("cancelStream = false, want true")
	}

	select {
	case <-opCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("operation context was not cancelled")
	}

	if opCtx.Err() == nil {
		t.Fatal("operation context should be cancelled by explicit stream cancel")
	}
}
