package openai

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
)

const (
	contractToken  = "sk-contract-test-token"
	contractModel  = "gpt-4o-mini"
	contractSystem = "You answer weather questions using the provided tool."
	contractPrompt = "What is the weather in Paris?"
	contractCallID = "call_weather_paris"
)

// TestProviderStreamToolLoopContract exercises the production Chat Completions
// adapter against a local scripted endpoint. It locks the wire format at the
// boundary where tool call deltas become the next turn's assistant/tool messages.
func TestProviderStreamToolLoopContract(t *testing.T) {
	toolSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"city": map[string]any{
				"type":        "string",
				"description": "The city to look up.",
			},
		},
		"required":             []any{"city"},
		"additionalProperties": false,
	}
	tools := []ai.ToolDefinition{{
		Name:        "lookup_weather",
		Description: "Look up the current weather for a city.",
		InputSchema: toolSchema,
	}}

	firstMessages := []any{
		map[string]any{"role": "system", "content": contractSystem},
		map[string]any{"role": "user", "content": contractPrompt},
	}
	server := newChatCompletionScriptServer([]chatCompletionScript{
		{
			request: chatCompletionRequest(contractModel, firstMessages, toolSchema),
			response: strings.Join([]string{
				`data: {"id":"chatcmpl-tool","object":"chat.completion.chunk","created":1,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_weather_paris","type":"function","function":{}}]},"finish_reason":null}]}`,
				`data: {"id":"chatcmpl-tool","object":"chat.completion.chunk","created":1,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"lookup_weather","arguments":"{\"city\":"}}]},"finish_reason":null}]}`,
				`data: {"id":"chatcmpl-tool","object":"chat.completion.chunk","created":1,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"Paris\"}"}}]},"finish_reason":null}]}`,
				`data: {"id":"chatcmpl-tool","object":"chat.completion.chunk","created":1,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
				"data: [DONE]",
			}, "\n\n") + "\n\n",
		},
		{
			request: func(body map[string]any) error {
				assistant := map[string]any{
					"role": "assistant",
					"tool_calls": []any{map[string]any{
						"id":   contractCallID,
						"type": "function",
						"function": map[string]any{
							"name":      "lookup_weather",
							"arguments": `{"city":"Paris"}`,
						},
					}},
				}
				messages := append(append([]any{}, firstMessages...), assistant, map[string]any{
					"role":         "tool",
					"tool_call_id": contractCallID,
					"content":      "weather=21C",
				})
				return chatCompletionRequest(contractModel, messages, toolSchema)(body)
			},
			response: strings.Join([]string{
				`data: {"id":"chatcmpl-final","object":"chat.completion.chunk","created":2,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"role":"assistant","content":"The weather in "},"finish_reason":null}]}`,
				`data: {"id":"chatcmpl-final","object":"chat.completion.chunk","created":2,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"content":"Paris is 21C."},"finish_reason":null}]}`,
				`data: {"id":"chatcmpl-final","object":"chat.completion.chunk","created":2,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
				"data: [DONE]",
			}, "\n\n") + "\n\n",
		},
	})
	defer server.Close()
	t.Cleanup(func() { server.assertConsumed(t) })

	provider := New(Config{APIKey: contractToken, BaseURL: server.URL + "/v1"})
	model := ai.Model{Name: contractModel}
	firstContext := ai.Context{
		System:   contractSystem,
		Messages: []ai.Message{ai.UserMessage{Content: contractPrompt}},
		Tools:    tools,
	}

	firstEvents := streamEvents(t, provider, model, firstContext)
	assertEventKinds(t, firstEvents, "start", "toolcall_delta", "toolcall_delta", "toolcall_delta", "stop")
	firstDeltas := toolCallDeltas(t, firstEvents[1:4])
	for i, delta := range firstDeltas {
		if delta.ID != contractCallID {
			t.Fatalf("turn 1 tool delta %d ID = %q, want %q", i, delta.ID, contractCallID)
		}
	}
	if firstDeltas[0].Name != "" || firstDeltas[0].Arguments != "" {
		t.Fatalf("turn 1 ID delta = %+v, want only ID", firstDeltas[0])
	}
	if firstDeltas[1].Name != "lookup_weather" || firstDeltas[1].Arguments != `{"city":` {
		t.Fatalf("turn 1 name/argument delta = %+v", firstDeltas[1])
	}
	if firstDeltas[2].Name != "" || firstDeltas[2].Arguments != `"Paris"}` {
		t.Fatalf("turn 1 final argument delta = %+v", firstDeltas[2])
	}
	callName := firstDeltas[1].Name
	arguments := firstDeltas[0].Arguments + firstDeltas[1].Arguments + firstDeltas[2].Arguments
	if callName != "lookup_weather" {
		t.Fatalf("turn 1 tool name = %q, want lookup_weather", callName)
	}
	if arguments != `{"city":"Paris"}` {
		t.Fatalf("turn 1 concatenated arguments = %q", arguments)
	}
	if stop := firstEvents[len(firstEvents)-1].(ai.EventStop); stop.Reason != ai.StopReasonToolUse {
		t.Fatalf("turn 1 stop reason = %q, want %q", stop.Reason, ai.StopReasonToolUse)
	}

	var toolArguments map[string]any
	if err := json.Unmarshal([]byte(arguments), &toolArguments); err != nil {
		t.Fatalf("turn 1 tool arguments must be JSON: %v", err)
	}
	secondContext := ai.Context{
		System: contractSystem,
		Messages: []ai.Message{
			ai.UserMessage{Content: contractPrompt},
			ai.AssistantMessage{Content: []ai.ContentBlock{
				ai.ToolCall{ID: firstDeltas[0].ID, Name: callName, Arguments: toolArguments},
			}},
			ai.ToolResultMessage{
				ToolCallID: firstDeltas[0].ID,
				ToolName:   callName,
				Content:    []ai.ContentBlock{ai.TextContent{Text: "weather=21C"}},
			},
		},
		Tools: tools,
	}

	secondEvents := streamEvents(t, provider, model, secondContext)
	assertEventKinds(t, secondEvents, "start", "text_delta", "text_delta", "stop")
	if text := secondEvents[1].(ai.EventTextDelta).Text + secondEvents[2].(ai.EventTextDelta).Text; text != "The weather in Paris is 21C." {
		t.Fatalf("turn 2 text = %q", text)
	}
	if stop := secondEvents[3].(ai.EventStop); stop.Reason != ai.StopReasonStop {
		t.Fatalf("turn 2 stop reason = %q, want %q", stop.Reason, ai.StopReasonStop)
	}
}

func chatCompletionRequest(model string, messages []any, schema map[string]any) func(map[string]any) error {
	return func(body map[string]any) error {
		want := map[string]any{
			"model":          model,
			"stream":         true,
			"stream_options": map[string]any{"include_usage": true},
			"messages":       messages,
			"tools": []any{map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        "lookup_weather",
					"description": "Look up the current weather for a city.",
					"parameters":  schema,
				},
			}},
		}
		if !reflect.DeepEqual(body, want) {
			return fmt.Errorf("JSON body mismatch\n got: %#v\nwant: %#v", body, want)
		}
		return nil
	}
}

func streamEvents(t *testing.T, provider *Provider, model ai.Model, input ai.Context) []ai.AssistantEvent {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	stream, err := provider.Stream(ctx, model, input, ai.StreamOptions{})
	if err != nil {
		t.Fatalf("Provider.Stream() error = %v", err)
	}
	var events []ai.AssistantEvent
	for {
		select {
		case event, ok := <-stream.Events():
			if !ok {
				if err := stream.Wait(); err != nil {
					t.Fatalf("stream.Wait() error = %v", err)
				}
				return events
			}
			events = append(events, event)
		case <-ctx.Done():
			t.Fatalf("timed out collecting provider events: %v", ctx.Err())
		}
	}
}

func assertEventKinds(t *testing.T, events []ai.AssistantEvent, want ...string) {
	t.Helper()
	got := make([]string, 0, len(events))
	for _, event := range events {
		switch event.(type) {
		case ai.EventStart:
			got = append(got, "start")
		case ai.EventTextDelta:
			got = append(got, "text_delta")
		case ai.EventToolCallDelta:
			got = append(got, "toolcall_delta")
		case ai.EventStop:
			got = append(got, "stop")
		case ai.EventUsage:
			got = append(got, "usage")
		case ai.EventError:
			got = append(got, "error")
		default:
			got = append(got, fmt.Sprintf("%T", event))
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("event order = %v, want %v", got, want)
	}
}

func toolCallDeltas(t *testing.T, events []ai.AssistantEvent) []ai.EventToolCallDelta {
	t.Helper()
	deltas := make([]ai.EventToolCallDelta, 0, len(events))
	for _, event := range events {
		delta, ok := event.(ai.EventToolCallDelta)
		if !ok {
			t.Fatalf("event %T is not a tool call delta", event)
		}
		deltas = append(deltas, delta)
	}
	return deltas
}

type chatCompletionScript struct {
	request  func(map[string]any) error
	response string
}

type chatCompletionScriptServer struct {
	mu      sync.Mutex
	scripts []chatCompletionScript
	next    int
	errors  []string
}

type chatCompletionContractServer struct {
	*httptest.Server
	handler *chatCompletionScriptServer
}

func newChatCompletionScriptServer(scripts []chatCompletionScript) *chatCompletionContractServer {
	handler := &chatCompletionScriptServer{scripts: scripts}
	return &chatCompletionContractServer{
		Server:  httptest.NewServer(handler),
		handler: handler,
	}
}

func (s *chatCompletionContractServer) assertConsumed(t *testing.T) {
	s.handler.assertConsumed(t)
}

func (s *chatCompletionScriptServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if err := validateChatCompletionTransport(r); err != nil {
		s.recordError(err)
		http.Error(w, "contract request rejected", http.StatusBadRequest)
		return
	}
	body, err := decodeJSONBody(w, r)
	if err != nil {
		s.recordError(err)
		http.Error(w, "contract request rejected", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	if s.next >= len(s.scripts) {
		s.recordErrorLocked(fmt.Errorf("unscripted request %s %s", r.Method, r.URL.Path))
		s.mu.Unlock()
		http.Error(w, "unscripted request", http.StatusInternalServerError)
		return
	}
	script := s.scripts[s.next]
	s.next++
	scriptNumber := s.next
	s.mu.Unlock()

	if err := script.request(body); err != nil {
		s.recordError(fmt.Errorf("script %d: %w", scriptNumber, err))
		http.Error(w, "contract request rejected", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, script.response)
}

func validateChatCompletionTransport(r *http.Request) error {
	if r.Method != http.MethodPost {
		return fmt.Errorf("method = %q, want POST", r.Method)
	}
	if r.URL.Path != "/v1/chat/completions" {
		return fmt.Errorf("path = %q, want /v1/chat/completions", r.URL.Path)
	}
	if got := r.Header.Get("Authorization"); got != "Bearer "+contractToken {
		return fmt.Errorf("Authorization = %q, want Bearer fake contract token", got)
	}
	if got := r.Header.Get("Content-Type"); got != "application/json" {
		return fmt.Errorf("Content-Type = %q, want application/json", got)
	}
	if got := r.Header.Get("Accept"); got != "application/json" {
		return fmt.Errorf("Accept = %q, want application/json", got)
	}
	return nil
}

func decodeJSONBody(w http.ResponseWriter, r *http.Request) (map[string]any, error) {
	defer func() { _ = r.Body.Close() }()
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	var body map[string]any
	if err := decoder.Decode(&body); err != nil {
		return nil, fmt.Errorf("decode request JSON: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("request body has multiple JSON values")
		}
		return nil, fmt.Errorf("decode trailing request JSON: %w", err)
	}
	return body, nil
}

func (s *chatCompletionScriptServer) recordError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recordErrorLocked(err)
}

func (s *chatCompletionScriptServer) recordErrorLocked(err error) {
	const maxErrors = 4
	if len(s.errors) < maxErrors {
		s.errors = append(s.errors, err.Error())
	}
}

func (s *chatCompletionScriptServer) assertConsumed(t *testing.T) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.errors) > 0 {
		t.Errorf("scripted Chat Completions server errors: %s", strings.Join(s.errors, "; "))
	}
	if s.next != len(s.scripts) {
		t.Errorf("consumed %d scripted requests, want %d", s.next, len(s.scripts))
	}
}
