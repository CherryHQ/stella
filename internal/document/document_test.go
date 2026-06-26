package document

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestNormalizeResultRejectsEmptyContent(t *testing.T) {
	if _, err := NormalizeResult(&Result{Content: " \n\t "}); !errors.Is(err, ErrEmptyContent) {
		t.Fatalf("NormalizeResult empty error = %v, want ErrEmptyContent", err)
	}
}

func TestNormalizeResultTrimsContent(t *testing.T) {
	result, err := NormalizeResult(&Result{Content: " hello \n"})
	if err != nil {
		t.Fatalf("NormalizeResult: %v", err)
	}
	if result.Content != "hello" {
		t.Fatalf("content = %q, want hello", result.Content)
	}
}

func TestWithTimeoutCancels(t *testing.T) {
	ctx, cancel := WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("context did not cancel")
	}
}

func TestWrapUnavailable(t *testing.T) {
	err := WrapUnavailable(errors.New("missing binary"))
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("WrapUnavailable = %v, want ErrUnavailable", err)
	}
}
