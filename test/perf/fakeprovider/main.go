// Command fakeprovider is a deterministic stand-in for the Anthropic Messages
// API, used by the chat perf harness (test/perf/run.sh). Point a stella
// provider's base_url at it and every model call resolves locally with a
// scripted, repeatable response — no network, no secrets, no token variance.
//
// The last user message selects the behavior:
//
//   - "seed ..."   → one fixed markdown reply, emitted instantly (fixture seeding)
//   - "stream ..." → a long reply streamed as PERF_STREAM_CHUNKS text deltas at
//     PERF_STREAM_INTERVAL_MS cadence (defaults 1500 × 10ms), ending
//     with the END-OF-STREAM sentinel the harness polls for
//   - anything else → a short instant reply (absorbs title-gen or other
//     background model calls without failing)
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

func main() {
	port := flag.Int("port", 25901, "listen port")
	flag.Parse()

	http.HandleFunc("/", handle)
	addr := fmt.Sprintf("127.0.0.1:%d", *port)
	log.Printf("fakeprovider listening on http://%s (chunks=%d interval=%dms)",
		addr, streamChunks(), streamIntervalMS())
	log.Fatal(http.ListenAndServe(addr, nil))
}

func envInt(name string, def int) int {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

func streamChunks() int     { return envInt("PERF_STREAM_CHUNKS", 1500) }
func streamIntervalMS() int { return envInt("PERF_STREAM_INTERVAL_MS", 10) }

// anthropicReq is the subset of the Messages API request the fake reads.
type anthropicReq struct {
	Model    string `json:"model"`
	Messages []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"messages"`
}

// lastUserText extracts the text of the final user message; content may be a
// plain string or an array of content blocks.
func lastUserText(req anthropicReq) string {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role != "user" {
			continue
		}
		raw := req.Messages[i].Content
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return s
		}
		var blocks []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(raw, &blocks) == nil {
			for _, b := range blocks {
				if b.Type == "text" {
					return b.Text
				}
			}
		}
		return ""
	}
	return ""
}

func handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	var req anthropicReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	text := lastUserText(req)
	// stellad prefixes user turns with a "ts:<epoch>" line; strip it so the
	// scenario keyword is at the start.
	if strings.HasPrefix(text, "ts:") {
		if _, rest, ok := strings.Cut(text, "\n"); ok {
			text = rest
		}
	}
	log.Printf("request: model=%s lastUser=%.120q", req.Model, text)
	switch {
	case strings.HasPrefix(text, "stream"):
		// Echo the rest of the user text into the closing sentinel so the
		// harness can distinguish this reply from earlier streamed turns
		// already in the transcript.
		nonce := strings.TrimSpace(strings.TrimPrefix(text, "stream"))
		stream(w, req.Model, streamChunkTexts(nonce), time.Duration(streamIntervalMS())*time.Millisecond)
	case strings.HasPrefix(text, "seed"):
		stream(w, req.Model, []string{seedReply}, 0)
	default:
		stream(w, req.Model, []string{"ok."}, 0)
	}
}

// stream writes a complete Anthropic SSE turn: message_start, one text content
// block whose deltas are the given chunks (paced by interval), then the
// closing frames. Framing mirrors test/system/fake_anthropic_test.go.
func stream(w http.ResponseWriter, model string, chunks []string, interval time.Duration) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "no flusher", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)

	emit := func(event string, data map[string]any) {
		payload, err := json.Marshal(data)
		if err != nil {
			panic(fmt.Sprintf("fakeprovider: marshal %s: %v", event, err))
		}
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, payload)
		fl.Flush()
	}

	emit("message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id": "msg_perf", "type": "message", "role": "assistant",
			"model": model, "content": []any{},
			"stop_reason": nil, "stop_sequence": nil,
			"usage": map[string]any{"input_tokens": 1, "output_tokens": 1},
		},
	})
	emit("content_block_start", map[string]any{
		"type": "content_block_start", "index": 0,
		"content_block": map[string]any{"type": "text", "text": ""},
	})
	for i, c := range chunks {
		if interval > 0 && i > 0 {
			time.Sleep(interval)
		}
		emit("content_block_delta", map[string]any{
			"type": "content_block_delta", "index": 0,
			"delta": map[string]any{"type": "text_delta", "text": c},
		})
	}
	emit("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
	emit("message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil},
		"usage": map[string]any{"output_tokens": 5},
	})
	emit("message_stop", map[string]any{"type": "message_stop"})
}

// streamChunkTexts builds the deterministic word-sized deltas for a streamed
// reply. Every 150th chunk opens or closes a code fence so the answer exercises
// markdown block splitting and syntax highlighting, and the final chunk carries
// the sentinel the harness polls for.
func streamChunkTexts(nonce string) []string {
	words := strings.Fields("the quick brown fox jumps over the lazy dog while " +
		"parsing markdown blocks and rendering code fences under streaming load")
	n := streamChunks()
	chunks := make([]string, 0, n)
	inFence := false
	for i := 0; i < n-1; i++ {
		switch {
		case i%150 == 149:
			if inFence {
				chunks = append(chunks, "\n```\n\n")
			} else {
				chunks = append(chunks, "\n\n```go\nfunc perf() {\n")
			}
			inFence = !inFence
		case inFence:
			chunks = append(chunks, fmt.Sprintf("\tx%d := %d\n", i, i))
		default:
			chunks = append(chunks, words[i%len(words)]+" ")
		}
	}
	tail := "\n\nEND-OF-STREAM " + nonce
	if inFence {
		tail = "\n```" + tail
	}
	chunks = append(chunks, tail)
	return chunks
}

// seedReply is the fixed assistant turn used to build the long-history
// fixture: representative markdown (heading, list, code block, prose) so the
// transcript renders realistic bubbles.
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
