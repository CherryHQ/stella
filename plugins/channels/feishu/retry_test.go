package feishu

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestSendCardReplyRetriesOnlyTransientFailures(t *testing.T) {
	t.Run("transient", func(t *testing.T) {
		attempts := 0
		pauses := 0
		bot := &Bot{
			replyCardFn: func(context.Context, string, string) (string, error) {
				attempts++
				if attempts < feishuSendAttempts {
					return "", context.DeadlineExceeded
				}
				return "om_sent", nil
			},
			retryPauseFn: func(context.Context, time.Duration) error {
				pauses++
				return nil
			},
		}
		messageID, err := bot.sendCardReply(context.Background(), "om_request", "hello", false)
		if err != nil || messageID != "om_sent" {
			t.Fatalf("sendCardReply = %q, %v", messageID, err)
		}
		if attempts != feishuSendAttempts || pauses != feishuSendAttempts-1 {
			t.Fatalf("attempts=%d pauses=%d, want %d/%d", attempts, pauses, feishuSendAttempts, feishuSendAttempts-1)
		}
	})

	t.Run("card build errors fail fast", func(t *testing.T) {
		original := buildCardContent
		buildCardContent = func(string) (string, error) { return "", errors.New("invalid card") }
		defer func() { buildCardContent = original }()
		attempts := 0
		bot := &Bot{replyCardFn: func(context.Context, string, string) (string, error) {
			attempts++
			return "", nil
		}}
		_, err := bot.sendCardReply(context.Background(), "om_request", "hello", false)
		if err == nil || attempts != 0 {
			t.Fatalf("card build error retried or succeeded: attempts=%d err=%v", attempts, err)
		}
	})

	t.Run("permanent API errors fail fast", func(t *testing.T) {
		attempts := 0
		bot := &Bot{replyCardFn: func(context.Context, string, string) (string, error) {
			attempts++
			return "", &feishuAPIError{code: 400, msg: "bad request"}
		}}
		_, err := bot.sendCardReply(context.Background(), "om_request", "hello", false)
		if err == nil || attempts != 1 {
			t.Fatalf("permanent API failure attempts=%d err=%v", attempts, err)
		}
	})
}

func TestRetryFeishuSendHonorsBoundedRetryAfter(t *testing.T) {
	attempts := 0
	var gotDelay time.Duration
	bot := &Bot{retryPauseFn: func(_ context.Context, delay time.Duration) error {
		gotDelay = delay
		return nil
	}}
	err := bot.retryFeishuSend(t.Context(), "patch card", func(context.Context) error {
		attempts++
		if attempts == 1 {
			return &feishuAPIError{code: 99991400, msg: "rate limited", retryAfter: 1500 * time.Millisecond}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("retry send: %v", err)
	}
	if gotDelay != 1500*time.Millisecond {
		t.Fatalf("delay = %v, want 1.5s", gotDelay)
	}
	if got := parseRetryAfter("30"); got != maxFeishuRetryAfter {
		t.Fatalf("bounded Retry-After = %v, want %v", got, maxFeishuRetryAfter)
	}
}

func TestParseRetryAfterSupportsHTTPDate(t *testing.T) {
	originalNow := nowFunc
	now := time.Date(2026, time.September, 4, 10, 0, 0, 0, time.UTC)
	nowFunc = func() time.Time { return now }
	t.Cleanup(func() { nowFunc = originalNow })

	if got := parseRetryAfter(now.Add(1500 * time.Millisecond).Format(http.TimeFormat)); got != time.Second {
		t.Fatalf("HTTP-date Retry-After = %v, want 1s after HTTP timestamp rounding", got)
	}
}

func TestRetryFeishuSendMarksTimeoutOutcomeUnknown(t *testing.T) {
	bot := &Bot{retryPauseFn: func(context.Context, time.Duration) error { return nil }}
	err := bot.retryFeishuSend(t.Context(), "reply card", func(context.Context) error {
		return context.DeadlineExceeded
	})
	var deliveryErr *feishuDeliveryError
	if !errors.As(err, &deliveryErr) {
		t.Fatalf("error = %T, want feishuDeliveryError", err)
	}
	if deliveryErr.outcome != deliveryUnknown {
		t.Fatalf("outcome = %q, want unknown", deliveryErr.outcome)
	}
}

func TestRetryFeishuSendMarksPermanentAPIOutcomeNotSent(t *testing.T) {
	bot := &Bot{}
	err := bot.retryFeishuSend(t.Context(), "reply card", func(context.Context) error {
		return &feishuAPIError{code: 400, msg: "bad request"}
	})
	var deliveryErr *feishuDeliveryError
	if !errors.As(err, &deliveryErr) {
		t.Fatalf("error = %T, want feishuDeliveryError", err)
	}
	if deliveryErr.outcome != deliveryNotSent {
		t.Fatalf("outcome = %q, want not_sent", deliveryErr.outcome)
	}
}

func TestReplyMessageBodyMarksThreadReplies(t *testing.T) {
	threadReply := replyMessageBody("interactive", `{}`, true)
	if threadReply.ReplyInThread == nil || !*threadReply.ReplyInThread {
		t.Fatalf("thread reply body = %#v, want reply_in_thread=true", threadReply)
	}
	plainReply := replyMessageBody("interactive", `{}`, false)
	if plainReply.ReplyInThread != nil {
		t.Fatalf("non-thread reply body = %#v, want omitted reply_in_thread", plainReply)
	}
}

func TestFinalDeliveryFailurePatchesVisibleTerminalNotice(t *testing.T) {
	var patches []string
	bot := &Bot{patchCardFn: func(_ context.Context, _ string, content string) error {
		patches = append(patches, content)
		if len(patches) == 1 {
			return errors.New("connection reset")
		}
		return nil
	}}
	err := bot.sendFinalResponseInThread(context.Background(), "oc_chat", "om_request", "", "om_progress", "answer", nil, false, true)
	if err == nil {
		t.Fatal("final response unexpectedly succeeded")
	}
	if len(patches) != 2 || !strings.Contains(patches[1], "delivery failed") {
		t.Fatalf("patches = %v, want final failure notice after failed final patch", patches)
	}
}

func TestNonFinalGroupDeliveryFailureDoesNotClaimTerminalState(t *testing.T) {
	var patches []string
	bot := &Bot{patchCardFn: func(_ context.Context, _ string, content string) error {
		patches = append(patches, content)
		return errors.New("connection reset")
	}}
	err := bot.sendFinalResponseInThread(context.Background(), "oc_chat", "om_request", "", "om_progress", "answer", nil, true, false)
	if err == nil {
		t.Fatal("non-final delivery failure unexpectedly succeeded")
	}
	if len(patches) != 1 || strings.Contains(patches[0], "delivery failed") {
		t.Fatalf("patches = %v, want only the failed answer patch before dispatcher retry", patches)
	}
}

func TestOverflowFailureDoesNotOverwriteDeliveredFirstChunk(t *testing.T) {
	var patches []string
	replies := 0
	bot := &Bot{
		patchCardFn: func(_ context.Context, _ string, content string) error {
			patches = append(patches, content)
			return nil
		},
		replyCardFn: func(_ context.Context, _ string, _ string) (string, error) {
			replies++
			if replies == 1 {
				return "", errors.New("connection reset")
			}
			return "om_failure", nil
		},
	}
	response := strings.Repeat("x", feishuMaxMessageLen+1)
	err := bot.sendFinalResponseInThread(context.Background(), "oc_chat", "om_request", "", "om_progress", response, nil, false, true)
	if err == nil {
		t.Fatal("overflow delivery unexpectedly succeeded")
	}
	if len(patches) != 1 || strings.Contains(patches[0], "delivery failed") {
		t.Fatalf("patches = %v, want delivered first chunk to remain intact", patches)
	}
	if replies != 2 {
		t.Fatalf("reply calls = %d, want failed overflow plus appended terminal notice", replies)
	}
}
