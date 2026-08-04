package openairesponse

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/providers"
)

const (
	contractAPIKey = "fake-openai-key"
	contractModel  = "gpt-4.1-mini"
	contractSystem = "You are a concise weather assistant."
	contractPrompt = "What is the weather in Paris?"
)

// TestProviderStreamContractToolLoop exercises the production adapter against a
// local Responses endpoint. It locks the request and normalized-event contracts
// for the function-call round trip without credentials or a public endpoint.
func TestProviderStreamContractToolLoop(t *testing.T) {
	tool := ai.ToolDefinition{
		Name:        "lookup_weather",
		Description: "Look up the current weather for a city.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"city": map[string]any{"type": "string"},
			},
			"required": []any{"city"},
		},
	}

	scripts := &responsesScripts{scripts: []responseScript{
		{
			body: responseRequest([]any{
				map[string]any{"role": "user", "content": contractPrompt},
			}, tool),
			events: []string{
				`{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"id":"fc_item_weather_1","type":"function_call","call_id":"call_weather_1","name":"lookup_weather","arguments":"","status":"in_progress"}}`,
				`{"type":"response.function_call_arguments.delta","sequence_number":2,"item_id":"fc_item_weather_1","output_index":0,"delta":"{\"city\":\""}`,
				`{"type":"response.function_call_arguments.delta","sequence_number":3,"item_id":"fc_item_weather_1","output_index":0,"delta":"Paris\"}"}`,
				`{"type":"response.function_call_arguments.done","sequence_number":4,"item_id":"fc_item_weather_1","output_index":0,"arguments":"{\"city\":\"Paris\"}"}`,
				completedResponseEvent("resp_weather_1", 11, 6, 17),
			},
		},
		{
			body: responseRequest([]any{
				map[string]any{"role": "user", "content": contractPrompt},
				map[string]any{
					"type":      "function_call",
					"call_id":   "call_weather_1",
					"name":      "lookup_weather",
					"arguments": `{"city":"Paris"}`,
				},
				map[string]any{
					"type":    "function_call_output",
					"call_id": "call_weather_1",
					"output":  "weather=21C",
				},
			}, tool),
			events: []string{
				`{"type":"response.output_text.delta","sequence_number":1,"item_id":"msg_weather_2","output_index":0,"content_index":0,"delta":"Paris is "}`,
				`{"type":"response.output_text.delta","sequence_number":2,"item_id":"msg_weather_2","output_index":0,"content_index":0,"delta":"21C."}`,
				completedResponseEvent("resp_weather_2", 20, 4, 24),
			},
		},
	}}
	server := httptest.NewServer(http.HandlerFunc(scripts.serveHTTP))
	t.Cleanup(func() {
		server.Close()
		if err := scripts.consumed(); err != nil {
			t.Error(err)
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	provider := New(Config{APIKey: contractAPIKey, BaseURL: server.URL + "/v1"})
	model := ai.Model{Name: contractModel}
	firstContext := ai.Context{
		System:   contractSystem,
		Messages: []ai.Message{ai.UserMessage{Content: contractPrompt}},
		Tools:    []ai.ToolDefinition{tool},
	}

	first, err := provider.Stream(ctx, model, firstContext, ai.StreamOptions{})
	if err != nil {
		t.Fatalf("start first stream: %v", err)
	}
	firstEvents, err := collectContractEvents(ctx, first)
	if err != nil {
		t.Fatalf("collect first stream: %v", err)
	}
	call := assertFirstTurn(t, firstEvents)

	secondContext := ai.Context{
		System: contractSystem,
		Messages: []ai.Message{
			ai.UserMessage{Content: contractPrompt},
			ai.AssistantMessage{Content: []ai.ContentBlock{call}},
			ai.ToolResultMessage{
				ToolCallID: call.ID,
				ToolName:   call.Name,
				Content:    []ai.ContentBlock{ai.TextContent{Text: "weather=21C"}},
			},
		},
		Tools: []ai.ToolDefinition{tool},
	}
	second, err := provider.Stream(ctx, model, secondContext, ai.StreamOptions{})
	if err != nil {
		t.Fatalf("start second stream: %v", err)
	}
	secondEvents, err := collectContractEvents(ctx, second)
	if err != nil {
		t.Fatalf("collect second stream: %v", err)
	}
	assertSecondTurn(t, secondEvents)
}

func responseRequest(input []any, tool ai.ToolDefinition) map[string]any {
	return map[string]any{
		"instructions": contractSystem,
		"input":        input,
		"model":        contractModel,
		"stream":       true,
		"tools": []any{map[string]any{
			"type":        "function",
			"name":        tool.Name,
			"description": tool.Description,
			"parameters":  tool.InputSchema,
		}},
	}
}

func completedResponseEvent(id string, inputTokens, outputTokens, totalTokens int) string {
	return fmt.Sprintf(`{"type":"response.completed","sequence_number":5,"response":{"id":%q,"object":"response","created_at":1700000000,"status":"completed","model":%q,"output":[],"usage":{"input_tokens":%d,"input_tokens_details":{"cached_tokens":0},"output_tokens":%d,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":%d}}}`,
		id, contractModel, inputTokens, outputTokens, totalTokens)
}

type responseScript struct {
	body   map[string]any
	events []string
}

type responsesScripts struct {
	mu      sync.Mutex
	scripts []responseScript
	used    int
	diag    error
}

func (s *responsesScripts) serveHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := decodeContractRequest(r.Body)
	if err != nil {
		s.reject(w, fmt.Errorf("decode request body: %w", err))
		return
	}

	s.mu.Lock()
	if s.used >= len(s.scripts) {
		s.rejectLocked(w, fmt.Errorf("unscripted request %s %s", r.Method, r.URL.Path))
		s.mu.Unlock()
		return
	}

	script := s.scripts[s.used]
	if err := validateContractRequest(r, body, script.body); err != nil {
		s.rejectLocked(w, fmt.Errorf("script %d: %w", s.used+1, err))
		s.mu.Unlock()
		return
	}
	s.used++
	s.mu.Unlock()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	for _, event := range script.events {
		var envelope struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(event), &envelope); err != nil || envelope.Type == "" {
			s.reject(w, fmt.Errorf("scripted response event has no valid type: %w", err))
			return
		}
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", envelope.Type, event)
	}
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func decodeContractRequest(body io.Reader) (map[string]any, error) {
	decoder := json.NewDecoder(io.LimitReader(body, 64<<10))
	var request map[string]any
	if err := decoder.Decode(&request); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("multiple JSON values")
	}
	return request, nil
}

func validateContractRequest(r *http.Request, got, want map[string]any) error {
	if r.Method != http.MethodPost {
		return fmt.Errorf("method = %s, want POST", r.Method)
	}
	if r.URL.Path != "/v1/responses" {
		return fmt.Errorf("path = %s, want /v1/responses", r.URL.Path)
	}
	if r.Header.Get("Authorization") != "Bearer "+contractAPIKey {
		return fmt.Errorf("authorization does not carry the fixed bearer token")
	}
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		return fmt.Errorf("content type = %q, want application/json", r.Header.Get("Content-Type"))
	}
	if !reflect.DeepEqual(got, want) {
		return fmt.Errorf("request JSON does not match the scripted contract")
	}
	return nil
}

func (s *responsesScripts) reject(w http.ResponseWriter, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rejectLocked(w, err)
}

func (s *responsesScripts) rejectLocked(w http.ResponseWriter, err error) {
	if s.diag == nil {
		message := err.Error()
		if len(message) > 512 {
			message = message[:512]
		}
		s.diag = fmt.Errorf("responses contract server: %s", message)
	}
	http.Error(w, "contract request rejected", http.StatusBadRequest)
}

func (s *responsesScripts) consumed() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.diag != nil {
		return s.diag
	}
	if s.used != len(s.scripts) {
		return fmt.Errorf("responses contract scripts consumed = %d, want %d", s.used, len(s.scripts))
	}
	return nil
}

func collectContractEvents(ctx context.Context, stream providers.AssistantEventStream) ([]ai.AssistantEvent, error) {
	var events []ai.AssistantEvent
	for {
		select {
		case event, ok := <-stream.Events():
			if !ok {
				return events, stream.Wait()
			}
			events = append(events, event)
		case <-ctx.Done():
			_ = stream.Close()
			return nil, ctx.Err()
		}
	}
}

func assertFirstTurn(t *testing.T, events []ai.AssistantEvent) ai.ToolCall {
	t.Helper()
	if len(events) != 6 {
		t.Fatalf("first turn events = %d, want 6: %#v", len(events), events)
	}
	if _, ok := events[0].(ai.EventStart); !ok {
		t.Fatalf("first turn event 0 = %T, want EventStart", events[0])
	}
	first, ok := events[1].(ai.EventToolCallDelta)
	if !ok {
		t.Fatalf("first turn event 1 = %T, want EventToolCallDelta", events[1])
	}
	second, ok := events[2].(ai.EventToolCallDelta)
	if !ok {
		t.Fatalf("first turn event 2 = %T, want EventToolCallDelta", events[2])
	}
	third, ok := events[3].(ai.EventToolCallDelta)
	if !ok {
		t.Fatalf("first turn event 3 = %T, want EventToolCallDelta", events[3])
	}
	if first.ID != "call_weather_1" || first.Name != "lookup_weather" || first.Arguments != "" {
		t.Fatalf("first tool delta = %+v, want call ID/name without arguments", first)
	}
	if second.ID != first.ID || third.ID != first.ID || second.Name != "" || third.Name != "" {
		t.Fatalf("tool call identity drifted across deltas: %+v, %+v, %+v", first, second, third)
	}
	arguments := second.Arguments + third.Arguments
	if arguments != `{"city":"Paris"}` {
		t.Fatalf("tool arguments = %q, want %q", arguments, `{"city":"Paris"}`)
	}
	usage, ok := events[4].(ai.EventUsage)
	if !ok || usage.Usage != (ai.Usage{InputTokens: 11, OutputTokens: 6, TotalTokens: 17}) {
		t.Fatalf("first turn event 4 = %#v, want usage before stop", events[4])
	}
	stop, ok := events[5].(ai.EventStop)
	if !ok || stop.Reason != ai.StopReasonStop {
		t.Fatalf("first turn event 5 = %#v, want StopReasonStop", events[5])
	}

	var decoded map[string]any
	if err := json.Unmarshal([]byte(arguments), &decoded); err != nil {
		t.Fatalf("decode observed tool arguments: %v", err)
	}
	return ai.ToolCall{ID: first.ID, Name: first.Name, Arguments: decoded}
}

func assertSecondTurn(t *testing.T, events []ai.AssistantEvent) {
	t.Helper()
	if len(events) != 5 {
		t.Fatalf("second turn events = %d, want 5: %#v", len(events), events)
	}
	if _, ok := events[0].(ai.EventStart); !ok {
		t.Fatalf("second turn event 0 = %T, want EventStart", events[0])
	}
	first, ok := events[1].(ai.EventTextDelta)
	if !ok {
		t.Fatalf("second turn event 1 = %T, want EventTextDelta", events[1])
	}
	second, ok := events[2].(ai.EventTextDelta)
	if !ok {
		t.Fatalf("second turn event 2 = %T, want EventTextDelta", events[2])
	}
	if first.Text+second.Text != "Paris is 21C." {
		t.Fatalf("second turn text = %q, want %q", first.Text+second.Text, "Paris is 21C.")
	}
	usage, ok := events[3].(ai.EventUsage)
	if !ok || usage.Usage != (ai.Usage{InputTokens: 20, OutputTokens: 4, TotalTokens: 24}) {
		t.Fatalf("second turn event 3 = %#v, want usage before stop", events[3])
	}
	stop, ok := events[4].(ai.EventStop)
	if !ok || stop.Reason != ai.StopReasonStop {
		t.Fatalf("second turn event 4 = %#v, want StopReasonStop", events[4])
	}
}
