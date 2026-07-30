//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

const (
	fakeModelID      = "claude-release-browser"
	fakeFirstChunk   = "release browser "
	fakeSecondChunk  = "reply"
	fakeFullReply    = fakeFirstChunk + fakeSecondChunk
	fakeErrorMessage = "release browser provider failure"
	fakeGateBackstop = 30 * time.Second
)

type fakeAnthropic struct {
	listener net.Listener
	server   *http.Server
	done     chan error

	mu           sync.Mutex
	gateNext     bool
	failNextTurn bool
	failedBody   string
	activeGate   chan struct{}
	modelCalls   int
	messageCalls int
	failedCalls  int
	gateTimeouts int
}

type fakeSummary struct {
	SchemaVersion int `json:"schema_version"`
	ModelCalls    int `json:"model_calls"`
	MessageCalls  int `json:"message_calls"`
	FailedCalls   int `json:"failed_calls"`
	GateTimeouts  int `json:"gate_timeouts"`
}

func startFakeAnthropic() (*fakeAnthropic, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen for fake Anthropic server: %w", err)
	}
	fake := &fakeAnthropic{
		listener: listener,
		done:     make(chan error, 1),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/models", fake.handleModels)
	mux.HandleFunc("POST /v1/messages", fake.handleMessages)
	mux.HandleFunc("GET /fixtures/feed.xml", fake.handleFeed)
	mux.HandleFunc("GET /fixtures/article", fake.handleArticle)
	mux.HandleFunc("POST /control/gate", fake.handleGate)
	mux.HandleFunc("POST /control/error", fake.handleError)
	mux.HandleFunc("POST /control/release", fake.handleRelease)
	fake.server = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		fake.done <- fake.server.Serve(listener)
	}()
	return fake, nil
}

func (f *fakeAnthropic) URL() string {
	return "http://" + f.listener.Addr().String()
}

func (f *fakeAnthropic) Summary() fakeSummary {
	f.mu.Lock()
	defer f.mu.Unlock()
	return fakeSummary{
		SchemaVersion: 1,
		ModelCalls:    f.modelCalls,
		MessageCalls:  f.messageCalls,
		FailedCalls:   f.failedCalls,
		GateTimeouts:  f.gateTimeouts,
	}
}

func (f *fakeAnthropic) Close() error {
	f.mu.Lock()
	pendingControl := f.gateNext || f.failNextTurn || f.activeGate != nil
	if f.activeGate != nil {
		close(f.activeGate)
		f.activeGate = nil
	}
	f.gateNext = false
	f.failNextTurn = false
	f.failedBody = ""
	gateTimeouts := f.gateTimeouts
	f.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	shutdownErr := f.server.Shutdown(ctx)
	serveErr := <-f.done
	if errors.Is(serveErr, http.ErrServerClosed) {
		serveErr = nil
	}
	var validationErr error
	if pendingControl {
		validationErr = fmt.Errorf("fake Anthropic control remained pending")
	}
	if gateTimeouts > 0 {
		validationErr = errors.Join(validationErr, fmt.Errorf("fake Anthropic gate timed out %d time(s)", gateTimeouts))
	}
	return errors.Join(shutdownErr, serveErr, validationErr)
}

func (f *fakeAnthropic) handleModels(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	f.modelCalls++
	f.mu.Unlock()
	writeFakeJSON(w, http.StatusOK, map[string]any{
		"data": []map[string]any{{
			"id":           fakeModelID,
			"type":         "model",
			"display_name": "Release Browser Model",
			"created_at":   "2026-01-01T00:00:00Z",
		}},
		"has_more": false,
		"first_id": fakeModelID,
		"last_id":  fakeModelID,
	})
}

func (f *fakeAnthropic) handleMessages(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	if err != nil {
		writeFakeJSON(w, http.StatusBadRequest, map[string]any{
			"type":  "error",
			"error": map[string]string{"type": "invalid_request_error", "message": "invalid request"},
		})
		return
	}
	var request fakeMessagesRequest
	if err := json.Unmarshal(body, &request); err != nil || request.Model == "" {
		writeFakeJSON(w, http.StatusBadRequest, map[string]any{
			"type":  "error",
			"error": map[string]string{"type": "invalid_request_error", "message": "invalid request"},
		})
		return
	}

	f.mu.Lock()
	f.messageCalls++
	messageCall := f.messageCalls
	gated := f.gateNext
	f.gateNext = false
	failed := false
	if f.failNextTurn {
		// Provider SDKs retry the same request after a 5xx response. Keep
		// failing that logical turn, then recover when the request body changes.
		if f.failedBody == "" {
			f.failedBody = string(body)
		}
		if string(body) == f.failedBody {
			failed = true
			f.failedCalls++
		} else {
			f.failNextTurn = false
			f.failedBody = ""
		}
	}
	var gate chan struct{}
	if gated {
		gate = make(chan struct{})
		f.activeGate = gate
	}
	f.mu.Unlock()

	if failed {
		writeFakeJSON(w, http.StatusInternalServerError, map[string]any{
			"type": "error",
			"error": map[string]string{
				"type":    "api_error",
				"message": fakeErrorMessage,
			},
		})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unavailable", http.StatusInternalServerError)
		return
	}
	action := request.goalControlAction()
	if action != "" && !request.hasToolResult() {
		for _, frame := range fakeGoalControlFrames(request.Model, action, messageCall) {
			_, _ = io.WriteString(w, frame)
		}
		flusher.Flush()
		return
	}

	before, after := fakeTextFrames(request.Model, gated)
	for _, frame := range before {
		_, _ = io.WriteString(w, frame)
	}
	flusher.Flush()

	if gated {
		select {
		case <-gate:
		case <-time.After(fakeGateBackstop):
			f.mu.Lock()
			f.gateTimeouts++
			f.mu.Unlock()
		}
		f.mu.Lock()
		if f.activeGate == gate {
			f.activeGate = nil
		}
		f.mu.Unlock()
	}
	for _, frame := range after {
		_, _ = io.WriteString(w, frame)
	}
	flusher.Flush()
}

// fakeMessagesRequest retains only structural protocol fields. Goal stages are
// selected from the goal_control action enum, never from prompt wording.
type fakeMessagesRequest struct {
	Model    string `json:"model"`
	Messages []struct {
		Content json.RawMessage `json:"content"`
	} `json:"messages"`
	Tools []struct {
		Name        string `json:"name"`
		InputSchema struct {
			Properties struct {
				Action struct {
					Enum []string `json:"enum"`
				} `json:"action"`
			} `json:"properties"`
		} `json:"input_schema"`
	} `json:"tools"`
}

func (r fakeMessagesRequest) goalControlAction() string {
	for _, tool := range r.Tools {
		if tool.Name != "goal_control" {
			continue
		}
		for _, action := range tool.InputSchema.Properties.Action.Enum {
			if action == "decompose" || action == "submit" {
				return action
			}
		}
	}
	return ""
}

func (r fakeMessagesRequest) hasToolResult() bool {
	for _, message := range r.Messages {
		var blocks []struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(message.Content, &blocks); err == nil {
			for _, block := range blocks {
				if block.Type == "tool_result" {
					return true
				}
			}
		}
	}
	return false
}

func fakeGoalControlFrames(model, action string, call int) []string {
	var args map[string]any
	switch action {
	case "decompose":
		args = map[string]any{
			"action":  "decompose",
			"summary": "one deterministic release-browser child",
			"decomposition": map[string]any{
				"children": []map[string]any{{
					"key":      "release-browser-child",
					"title":    "Release browser child",
					"intent":   "Produce deterministic workflow evidence.",
					"kind":     "leaf",
					"required": true,
				}},
			},
		}
	case "submit":
		args = map[string]any{
			"action":  "submit",
			"summary": "deterministic release-browser result",
			"output":  map[string]any{"result": "release-browser"},
		}
	default:
		panic(fmt.Sprintf("unsupported fake goal_control action %q", action))
	}
	rawArgs, err := json.Marshal(args)
	if err != nil {
		panic(fmt.Sprintf("marshal fake goal_control %s arguments: %v", action, err))
	}
	toolID := fmt.Sprintf("toolu_release_browser_%s_%d", action, call)
	return []string{
		fakeSSE("message_start", map[string]any{
			"type": "message_start",
			"message": map[string]any{
				"id": fmt.Sprintf("msg_release_browser_%d", call), "type": "message", "role": "assistant",
				"model": model, "content": []any{}, "stop_reason": nil, "stop_sequence": nil,
				"usage": map[string]any{"input_tokens": 1, "output_tokens": 1},
			},
		}),
		fakeSSE("content_block_start", map[string]any{
			"type": "content_block_start", "index": 0,
			"content_block": map[string]any{
				"type": "tool_use", "id": toolID, "name": "goal_control", "input": map[string]any{},
			},
		}),
		fakeSSE("content_block_delta", map[string]any{
			"type": "content_block_delta", "index": 0,
			"delta": map[string]any{"type": "input_json_delta", "partial_json": string(rawArgs)},
		}),
		fakeSSE("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0}),
		fakeSSE("message_delta", map[string]any{
			"type":  "message_delta",
			"delta": map[string]any{"stop_reason": "tool_use", "stop_sequence": nil},
			"usage": map[string]any{"output_tokens": 5},
		}),
		fakeSSE("message_stop", map[string]any{"type": "message_stop"}),
	}
}

func (f *fakeAnthropic) handleFeed(w http.ResponseWriter, r *http.Request) {
	articleURL := "http://" + r.Host + "/fixtures/article"
	w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
	_, _ = fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
  <channel>
    <title>Release Browser Feed</title>
    <link>%s</link>
    <description>Deterministic loopback RSS fixture.</description>
    <item>
      <guid>release-browser-entry</guid>
      <title>Release Browser Feed Entry</title>
      <link>%s</link>
      <description>Deterministic entry for release testing.</description>
    </item>
  </channel>
</rss>
`, html.EscapeString(articleURL), html.EscapeString(articleURL))
}

func (f *fakeAnthropic) handleArticle(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, "<!doctype html><title>Release Browser Article</title><p>Deterministic article body.</p>")
}

func (f *fakeAnthropic) handleGate(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.gateNext || f.failNextTurn || f.activeGate != nil {
		writeFakeJSON(w, http.StatusConflict, map[string]string{"error": "control already pending"})
		return
	}
	f.gateNext = true
	w.WriteHeader(http.StatusNoContent)
}

func (f *fakeAnthropic) handleError(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.gateNext || f.failNextTurn || f.activeGate != nil {
		writeFakeJSON(w, http.StatusConflict, map[string]string{"error": "control already pending"})
		return
	}
	f.failNextTurn = true
	f.failedBody = ""
	w.WriteHeader(http.StatusNoContent)
}

func (f *fakeAnthropic) handleRelease(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.activeGate == nil {
		writeFakeJSON(w, http.StatusConflict, map[string]string{"error": "no active gate"})
		return
	}
	close(f.activeGate)
	f.activeGate = nil
	w.WriteHeader(http.StatusNoContent)
}

func fakeTextFrames(model string, gated bool) (before, after []string) {
	start := []string{
		fakeSSE("message_start", map[string]any{
			"type": "message_start",
			"message": map[string]any{
				"id": "msg_release_browser", "type": "message", "role": "assistant",
				"model": model, "content": []any{}, "stop_reason": nil, "stop_sequence": nil,
				"usage": map[string]any{"input_tokens": 1, "output_tokens": 1},
			},
		}),
		fakeSSE("content_block_start", map[string]any{
			"type": "content_block_start", "index": 0,
			"content_block": map[string]any{"type": "text", "text": ""},
		}),
	}
	finish := []string{
		fakeSSE("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0}),
		fakeSSE("message_delta", map[string]any{
			"type":  "message_delta",
			"delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil},
			"usage": map[string]any{"output_tokens": 5},
		}),
		fakeSSE("message_stop", map[string]any{"type": "message_stop"}),
	}
	if gated {
		start = append(start, fakeTextDelta(fakeFirstChunk))
		before = start
		after = append([]string{fakeTextDelta(fakeSecondChunk)}, finish...)
		return before, after
	}
	start = append(start, fakeTextDelta(fakeFullReply))
	before = start
	return before, finish
}

func fakeTextDelta(text string) string {
	return fakeSSE("content_block_delta", map[string]any{
		"type": "content_block_delta", "index": 0,
		"delta": map[string]any{"type": "text_delta", "text": text},
	})
}

func fakeSSE(event string, value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Sprintf("marshal fake Anthropic %s event: %v", event, err))
	}
	return fmt.Sprintf("event: %s\ndata: %s\n\n", event, data)
}

func writeFakeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
