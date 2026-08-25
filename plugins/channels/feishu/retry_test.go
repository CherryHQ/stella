package feishu

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestSendCardReplyNeverRetriesOutcomeUnknownFailures(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		attempts := 0
		bot := &Bot{
			replyCardFn: func(context.Context, string, string) (string, error) {
				attempts++
				return "", context.DeadlineExceeded
			},
		}
		_, err := bot.sendCardReply(context.Background(), "om_request", "hello", false)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("sendCardReply error = %v, want deadline exceeded", err)
		}
		if attempts != 1 {
			t.Fatalf("attempts=%d, want one outcome-unknown request", attempts)
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

func TestFinalDeliveryFailureDoesNotAttemptFailureNotice(t *testing.T) {
	var patches []string
	bot := &Bot{patchCardFn: func(_ context.Context, _ string, content string) error {
		patches = append(patches, content)
		return errors.New("connection reset")
	}}
	err := bot.sendFinalResponseInThread(context.Background(), "oc_chat", "om_request", "", "om_progress", "answer", nil, false, true)
	if err == nil {
		t.Fatal("final response unexpectedly succeeded")
	}
	if len(patches) != 1 || strings.Contains(patches[0], "delivery failed") {
		t.Fatalf("patches = %v, want exactly one outcome-unknown request", patches)
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
	if replies != 1 {
		t.Fatalf("reply calls = %d, want one failed overflow request", replies)
	}
}
