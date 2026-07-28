//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	fakeGateBackstop = 30 * time.Second
)

type fakeAnthropic struct {
	listener net.Listener
	server   *http.Server
	done     chan error

	mu           sync.Mutex
	gateNext     bool
	activeGate   chan struct{}
	modelCalls   int
	messageCalls int
	gateTimeouts int
}

type fakeSummary struct {
	SchemaVersion int `json:"schema_version"`
	ModelCalls    int `json:"model_calls"`
	MessageCalls  int `json:"message_calls"`
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
	mux.HandleFunc("POST /control/gate", fake.handleGate)
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
		GateTimeouts:  f.gateTimeouts,
	}
}

func (f *fakeAnthropic) Close() error {
	f.mu.Lock()
	pendingGate := f.gateNext || f.activeGate != nil
	if f.activeGate != nil {
		close(f.activeGate)
		f.activeGate = nil
	}
	f.gateNext = false
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
	if pendingGate {
		validationErr = fmt.Errorf("fake Anthropic gate remained pending")
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
	var request struct {
		Model string `json:"model"`
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, 2<<20))
	if err := decoder.Decode(&request); err != nil || request.Model == "" {
		writeFakeJSON(w, http.StatusBadRequest, map[string]any{
			"type":  "error",
			"error": map[string]string{"type": "invalid_request_error", "message": "invalid request"},
		})
		return
	}

	f.mu.Lock()
	f.messageCalls++
	gated := f.gateNext
	f.gateNext = false
	var gate chan struct{}
	if gated {
		gate = make(chan struct{})
		f.activeGate = gate
	}
	f.mu.Unlock()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unavailable", http.StatusInternalServerError)
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

func (f *fakeAnthropic) handleGate(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.gateNext || f.activeGate != nil {
		writeFakeJSON(w, http.StatusConflict, map[string]string{"error": "gate already pending"})
		return
	}
	f.gateNext = true
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
