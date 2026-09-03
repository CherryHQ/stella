package fakeanthropic

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// MessageHandler serves deterministic seed/stream/default responses for the
// performance harness. It follows the same Anthropic Messages SSE contract as
// the scripted fake.
type MessageHandlerOptions struct {
	StreamChunks     int
	StreamIntervalMS int
}

func MessageHandler() http.Handler {
	return MessageHandlerWithOptions(MessageHandlerOptions{StreamChunks: 1500, StreamIntervalMS: 10})
}

func MessageHandlerWithOptions(options MessageHandlerOptions) http.Handler {
	if options.StreamChunks < 1 {
		options.StreamChunks = 1500
	}
	if options.StreamIntervalMS < 1 {
		options.StreamIntervalMS = 10
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { messageHandler(w, r, options) })
}

func messageHandler(w http.ResponseWriter, r *http.Request, options MessageHandlerOptions) {
	if r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	var req struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	text := ""
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role != "user" {
			continue
		}
		var s string
		if json.Unmarshal(req.Messages[i].Content, &s) == nil {
			text = s
		} else {
			var blocks []struct{ Type, Text string }
			_ = json.Unmarshal(req.Messages[i].Content, &blocks)
			for _, b := range blocks {
				if b.Type == "text" {
					text = b.Text
					break
				}
			}
		}
		break
	}
	if strings.HasPrefix(text, "ts:") {
		if _, rest, ok := strings.Cut(text, "\n"); ok {
			text = rest
		}
	}
	var chunks []string
	switch {
	case strings.HasPrefix(text, "stream"):
		chunks = messageStreamChunks(strings.TrimSpace(strings.TrimPrefix(text, "stream")), options.StreamChunks)
	case strings.HasPrefix(text, "seed"):
		chunks = []string{seedReply}
	default:
		chunks = []string{"ok."}
	}
	writeMessageStream(w, req.Model, chunks, func() time.Duration { return time.Duration(options.StreamIntervalMS) * time.Millisecond })
}

func writeMessageStream(w http.ResponseWriter, model string, chunks []string, interval func() time.Duration) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "no flusher", 500)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(200)
	emit := func(event string, data map[string]any) {
		b, _ := json.Marshal(data)
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
		fl.Flush()
	}
	emit("message_start", map[string]any{"type": "message_start", "message": map[string]any{"id": "msg_perf", "type": "message", "role": "assistant", "model": model, "content": []any{}, "stop_reason": nil, "stop_sequence": nil, "usage": map[string]any{"input_tokens": 1, "output_tokens": 1}}})
	emit("content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
	delay := interval()
	for i, c := range chunks {
		if delay > 0 && i > 0 {
			time.Sleep(delay)
		}
		emit("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": c}})
	}
	emit("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
	emit("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil}, "usage": map[string]any{"output_tokens": 5}})
	emit("message_stop", map[string]any{"type": "message_stop"})
}

func messageStreamChunks(nonce string, count int) []string {
	words := strings.Fields("the quick brown fox jumps over the lazy dog while parsing markdown blocks and rendering code fences under streaming load")
	n := max(count, 2)
	out := make([]string, 0, n)
	fence := false
	for i := 0; i < n-1; i++ {
		switch {
		case i%150 == 149 && fence:
			out = append(out, "\n```\n\n")
			fence = false
		case i%150 == 149:
			out = append(out, "\n\n```go\nfunc perf() {\n")
			fence = true
		case fence:
			out = append(out, fmt.Sprintf("\tx%d := %d\n", i, i))
		default:
			out = append(out, words[i%len(words)]+" ")
		}
	}
	tail := "\n\nEND-OF-STREAM " + nonce
	if fence {
		tail = "\n```" + tail
	}
	return append(out, tail)
}

const seedReply = `Here is a summary of the requested change.

## What happened

The build pipeline was updated to cache dependencies between runs, which
reduces cold-start time significantly for the common case.

- cache key derived from the lockfile hash
- fallback to the previous key on miss
- pruned automatically after seven days

` + "```go" + `
func cacheKey(lock []byte) string {
	sum := sha256.Sum256(lock)
	return "deps-" + hex.EncodeToString(sum[:8])
}
` + "```" + `

Verification: three consecutive CI runs hit the cache and completed in under
two minutes each. No further action is needed on this item.`
