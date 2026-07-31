package openairesponse

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/providers"
)

const openAIResponsesContractToken = "contract-dummy-token"

func TestProviderStreamHTTPContract(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "environment-key-must-not-be-used")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" {
			http.Error(w, "unexpected endpoint", http.StatusNotFound)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+openAIResponsesContractToken {
			http.Error(w, "unexpected authorization", http.StatusUnauthorized)
			return
		}
		if got := r.Header.Get("X-Stella-Contract"); got != "responses" {
			http.Error(w, "missing contract header", http.StatusBadRequest)
			return
		}

		var request struct {
			Model           string  `json:"model"`
			Stream          bool    `json:"stream"`
			Instructions    string  `json:"instructions"`
			Temperature     float64 `json:"temperature"`
			MaxOutputTokens int64   `json:"max_output_tokens"`
			Input           []struct {
				Role    string `json:"role"`
				Content any    `json:"content"`
			} `json:"input"`
			Tools []struct {
				Type       string         `json:"type"`
				Name       string         `json:"name"`
				Parameters map[string]any `json:"parameters"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "invalid JSON request", http.StatusBadRequest)
			return
		}
		if request.Model != "contract-model" || !request.Stream {
			http.Error(w, "unexpected model or stream option", http.StatusBadRequest)
			return
		}
		if request.Instructions != "contract system" ||
			request.Temperature != 0.25 ||
			request.MaxOutputTokens != 64 {
			http.Error(w, "unexpected generation options", http.StatusBadRequest)
			return
		}
		if len(request.Input) != 1 ||
			request.Input[0].Role != "user" ||
			request.Input[0].Content != "run the contract" {
			http.Error(w, "unexpected input", http.StatusBadRequest)
			return
		}
		if len(request.Tools) != 1 ||
			request.Tools[0].Type != "function" ||
			request.Tools[0].Name != "lookup" ||
			request.Tools[0].Parameters["type"] != "object" {
			http.Error(w, "unexpected tools", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		// The leading newline verifies Stella's proxy-compatibility middleware too.
		_, _ = fmt.Fprint(w, `
data: {"type":"response.output_text.delta","sequence_number":1,"item_id":"msg_contract","output_index":0,"content_index":0,"delta":"contract text","logprobs":[]}

data: {"type":"response.output_item.added","sequence_number":2,"output_index":1,"item":{"id":"fc_contract","type":"function_call","call_id":"call_contract","name":"lookup","arguments":"","status":"in_progress"}}

data: {"type":"response.function_call_arguments.delta","sequence_number":3,"item_id":"fc_contract","output_index":1,"delta":"{\"q\":\"stella\"}"}

data: {"type":"response.function_call_arguments.done","sequence_number":4,"item_id":"fc_contract","output_index":1,"arguments":"{\"q\":\"stella\"}"}

data: {"type":"response.completed","sequence_number":5,"response":{"id":"resp_contract","object":"response","created_at":1,"status":"completed","model":"contract-model","output":[],"parallel_tool_calls":false,"temperature":0.25,"tools":[],"top_p":1,"usage":{"input_tokens":7,"output_tokens":4,"total_tokens":11}}}

data: [DONE]

`)
	}))
	defer server.Close()

	temperature := 0.25
	maxTokens := 64
	provider := New(Config{
		BaseURL: server.URL + "/v1",
		APIKey:  openAIResponsesContractToken,
	})
	stream, err := provider.Stream(
		context.Background(),
		ai.Model{Name: "contract-model"},
		ai.Context{
			System:   "contract system",
			Messages: []ai.Message{ai.UserMessage{Content: "run the contract"}},
			Tools: []ai.ToolDefinition{{
				Name:        "lookup",
				Description: "Look up one value.",
				InputSchema: map[string]any{"type": "object"},
			}},
		},
		ai.StreamOptions{
			Temperature: &temperature,
			MaxTokens:   &maxTokens,
			Headers:     map[string]string{"X-Stella-Contract": "responses"},
		},
	)
	if err != nil {
		t.Fatalf("Stream() error = %v", err)
	}

	events := collectOpenAIResponsesContractEvents(t, stream)
	var text, arguments string
	var toolID, toolName string
	var usage ai.Usage
	var stop ai.StopReason
	for _, event := range events {
		switch event := event.(type) {
		case ai.EventTextDelta:
			text += event.Text
		case ai.EventToolCallDelta:
			if event.ID != "" {
				toolID = event.ID
			}
			if event.Name != "" {
				toolName = event.Name
			}
			arguments += event.Arguments
		case ai.EventUsage:
			usage = event.Usage
		case ai.EventStop:
			stop = event.Reason
		case ai.EventError:
			t.Fatalf("unexpected provider error event: %v", event.Err)
		}
	}
	if text != "contract text" {
		t.Fatalf("streamed text = %q", text)
	}
	if toolID != "call_contract" || toolName != "lookup" || arguments != `{"q":"stella"}` {
		t.Fatalf("tool call = id %q name %q arguments %q", toolID, toolName, arguments)
	}
	if stop != ai.StopReasonStop {
		t.Fatalf("stop reason = %q", stop)
	}
	if usage.InputTokens != 7 || usage.OutputTokens != 4 || usage.TotalTokens != 11 {
		t.Fatalf("usage = %+v", usage)
	}
}

func TestProviderStreamReportsHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = fmt.Fprint(w, `{"error":{"message":"contract rejected","type":"invalid_request_error"}}`)
	}))
	defer server.Close()

	provider := New(Config{BaseURL: server.URL + "/v1", APIKey: openAIResponsesContractToken})
	stream, err := provider.Stream(
		context.Background(),
		ai.Model{Name: "contract-model"},
		ai.Context{Messages: []ai.Message{ai.UserMessage{Content: "fail"}}},
		ai.StreamOptions{},
	)
	if err != nil {
		t.Fatalf("Stream() error = %v", err)
	}

	events := collectOpenAIResponsesContractEvents(t, stream)
	for _, event := range events {
		if providerError, ok := event.(ai.EventError); ok {
			if providerError.Err == nil || !strings.Contains(providerError.Err.Error(), "contract rejected") {
				t.Fatalf("provider error = %v", providerError.Err)
			}
			return
		}
	}
	t.Fatal("stream did not emit EventError")
}

func collectOpenAIResponsesContractEvents(t *testing.T, stream providers.AssistantEventStream) []ai.AssistantEvent {
	t.Helper()
	defer func() { _ = stream.Close() }()

	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()

	var events []ai.AssistantEvent
	for {
		select {
		case event, ok := <-stream.Events():
			if !ok {
				if err := stream.Wait(); err != nil {
					t.Fatalf("stream Wait() error = %v", err)
				}
				return events
			}
			events = append(events, event)
		case <-timer.C:
			t.Fatal("timed out waiting for provider stream")
		}
	}
}
