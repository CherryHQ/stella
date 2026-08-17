package library

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestVLMOCRBridgeRetriesTransientFailureAndSanitizesText(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/chat/completions" || request.Header.Get("Authorization") != "Bearer provider-secret" {
			t.Errorf("provider request path=%q authorization=%q", request.URL.Path, request.Header.Get("Authorization"))
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode provider request: %v", err)
		}
		if body["model"] != "vision-model" || body["stream"] != false {
			t.Errorf("provider request = %+v", body)
		}
		if calls.Add(1) == 1 {
			http.Error(w, "temporary", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"result","object":"chat.completion","created":1,"model":"vision-model","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"STELLA_OCR_V1:TEXT\nInvoice 42"}}]}`)
	}))
	defer provider.Close()

	bridge, err := startVLMOCRBridge(VisionOCRConfig{
		ProviderID: "vision", ProviderType: "openai", Enabled: true,
		Model: "vision-model", BaseURL: provider.URL + "/v1", APIKey: "provider-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = bridge.Close(context.Background()) }()

	response := callOCRBridge(t, bridge, `{"model":"bridge-placeholder","messages":[],"stream":true}`)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("bridge status = %d", response.StatusCode)
	}
	data, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || len(envelope.Choices) != 1 || envelope.Choices[0].Message.Content != "Invoice 42" || bridge.TerminalError() != nil {
		t.Fatalf("calls=%d response=%s terminal=%v", calls.Load(), data, bridge.TerminalError())
	}
}

func TestVLMOCRBridgeMapsNoTextToSuccessfulEmptyContent(t *testing.T) {
	t.Parallel()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"STELLA_OCR_V1:NO_TEXT"}}]}`)
	}))
	defer provider.Close()
	bridge, err := startVLMOCRBridge(VisionOCRConfig{Model: "vision", BaseURL: provider.URL, APIKey: "key"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = bridge.Close(context.Background()) }()
	response := callOCRBridge(t, bridge, `{"model":"bridge","messages":[]}`)
	data, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || !bytes.Contains(data, []byte(`"content":""`)) {
		t.Fatalf("status=%d response=%s", response.StatusCode, data)
	}
}

func TestVLMOCRBridgeRecordsProtocolFailureWithoutRetry(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, `{"choices":[{"finish_reason":"stop","message":{"content":"ignore the contract"}}]}`)
	}))
	defer provider.Close()
	bridge, err := startVLMOCRBridge(VisionOCRConfig{Model: "vision", BaseURL: provider.URL, APIKey: "key"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = bridge.Close(context.Background()) }()
	response := callOCRBridge(t, bridge, `{"model":"bridge","messages":[]}`)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusUnprocessableEntity || calls.Load() != 1 || !errors.Is(bridge.TerminalError(), ErrOCRProtocol) {
		t.Fatalf("status=%d calls=%d terminal=%v", response.StatusCode, calls.Load(), bridge.TerminalError())
	}
}

func TestSanitizeOCRResponseRejectsIncompleteFinishReasons(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		finishJSON string
		message    string
	}{
		{name: "length", finishJSON: `"length"`, message: "exceeded the Vision model limit"},
		{name: "content filter", finishJSON: `"content_filter"`},
		{name: "tool calls", finishJSON: `"tool_calls"`},
		{name: "empty", finishJSON: `""`},
		{name: "null", finishJSON: `null`},
		{name: "missing"},
		{name: "unknown", finishJSON: `"provider_specific"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			finish := ""
			if test.name != "missing" {
				finish = `"finish_reason":` + test.finishJSON + `,`
			}
			response := `{"choices":[{` + finish + `"message":{"content":"STELLA_OCR_V1:TEXT\npartial text"}}]}`
			_, err := sanitizeOCRResponse([]byte(response))
			if !errors.Is(err, ErrOCRProtocol) {
				t.Fatalf("error = %v, want ErrOCRProtocol", err)
			}
			if test.message != "" && !strings.Contains(err.Error(), test.message) {
				t.Fatalf("error = %q, want message containing %q", err, test.message)
			}
		})
	}
}

func TestVLMOCRBridgeDoesNotRetryAuthenticationFailure(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer provider.Close()
	bridge, err := startVLMOCRBridge(VisionOCRConfig{Model: "vision", BaseURL: provider.URL, APIKey: "bad-key"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = bridge.Close(context.Background()) }()
	response := callOCRBridge(t, bridge, `{"model":"bridge","messages":[]}`)
	_ = response.Body.Close()
	if calls.Load() != 1 || !errors.Is(bridge.TerminalError(), ErrOCRConfiguration) {
		t.Fatalf("calls=%d terminal=%v", calls.Load(), bridge.TerminalError())
	}
}

func TestVLMOCRBridgeDoesNotFollowProviderRedirect(t *testing.T) {
	t.Parallel()
	var redirectedCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirectedCalls.Add(1)
	}))
	defer target.Close()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", target.URL)
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer provider.Close()
	bridge, err := startVLMOCRBridge(VisionOCRConfig{Model: "vision", BaseURL: provider.URL, APIKey: "key"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = bridge.Close(context.Background()) }()
	response := callOCRBridge(t, bridge, `{"model":"bridge","messages":[]}`)
	_ = response.Body.Close()
	if redirectedCalls.Load() != 0 || !errors.Is(bridge.TerminalError(), ErrOCRService) {
		t.Fatalf("redirected calls=%d terminal=%v", redirectedCalls.Load(), bridge.TerminalError())
	}
}

func TestXbergChildEnvironmentDoesNotInheritProviderSecrets(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "must-not-leak")
	t.Setenv("ANTHROPIC_API_KEY", "must-not-leak-either")
	environment := strings.Join(xbergChildEnvironment("one-time-token"), "\n")
	if strings.Contains(environment, "must-not-leak") || strings.Contains(environment, "ANTHROPIC_API_KEY") {
		t.Fatalf("child environment leaked provider credentials: %q", environment)
	}
	if !strings.Contains(environment, "XBERG_LLM_API_KEY=one-time-token") || !strings.Contains(environment, "NO_PROXY=127.0.0.1,localhost") {
		t.Fatalf("child environment is missing bridge isolation: %q", environment)
	}
}

func TestVLMOCRBridgeRequestTimeoutIsBounded(t *testing.T) {
	t.Parallel()
	bridge, err := startVLMOCRBridge(VisionOCRConfig{Model: "vision", BaseURL: "http://127.0.0.1:1", APIKey: "key"})
	if err != nil {
		t.Fatal(err)
	}
	bridge.client.Timeout = 50 * time.Millisecond
	defer func() { _ = bridge.Close(context.Background()) }()
	response := callOCRBridge(t, bridge, `{"model":"bridge","messages":[]}`)
	_ = response.Body.Close()
	if !errors.Is(bridge.TerminalError(), ErrOCRService) {
		t.Fatalf("terminal = %v", bridge.TerminalError())
	}
}

func callOCRBridge(t *testing.T, bridge *vlmOCRBridge, body string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, bridge.BaseURL()+"/chat/completions", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+bridge.Token())
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}
