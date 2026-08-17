package library

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	ocrBridgePath          = "/v1/chat/completions"
	ocrBridgeRequestLimit  = 32 << 20
	ocrBridgeResponseLimit = 4 << 20
	ocrRequestTimeout      = 120 * time.Second
	ocrRetryDelay          = 250 * time.Millisecond
	ocrProtocolText        = "STELLA_OCR_V1:TEXT\n"
	ocrProtocolNoText      = "STELLA_OCR_V1:NO_TEXT"
)

var (
	ErrOCRPageLimit     = errors.New("OCR candidate page limit exceeded")
	ErrOCRConfiguration = errors.New("OCR provider configuration is invalid")
	ErrOCRProtocol      = errors.New("OCR provider response violates the protocol")
	ErrOCRService       = errors.New("OCR provider request failed")
)

// VisionOCRConfig is an attempt-local snapshot of the deployment Vision
// provider. APIKey is deliberately excluded from parser profiles and is never
// passed to the Xberg child process.
type VisionOCRConfig struct {
	ProviderID   string
	ProviderType string
	Enabled      bool
	Model        string
	BaseURL      string
	APIKey       string
}

// VisionOCRResolver loads one consistent provider snapshot for a parser
// attempt. Native-only PDFs can still parse when the returned config is empty.
type VisionOCRResolver func(context.Context) (VisionOCRConfig, error)

// ocrTerminalError distinguishes terminal provider/configuration failures from
// process and storage failures that River may safely retry.
type ocrTerminalError struct {
	kind    error
	message string
}

func (e *ocrTerminalError) Error() string { return e.message }
func (e *ocrTerminalError) Unwrap() error { return e.kind }

type vlmOCRBridge struct {
	server   *http.Server
	listener net.Listener
	token    string
	config   VisionOCRConfig
	client   *http.Client

	mu       sync.Mutex
	terminal error
}

func startVLMOCRBridge(config VisionOCRConfig) (*vlmOCRBridge, error) {
	endpoint, err := openAIChatCompletionsURL(config.BaseURL)
	if err != nil {
		return nil, err
	}
	config.BaseURL = endpoint
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, fmt.Errorf("generate OCR bridge token: %w", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen for OCR bridge: %w", err)
	}
	bridge := &vlmOCRBridge{
		listener: listener,
		token:    base64.RawURLEncoding.EncodeToString(tokenBytes),
		config:   config,
		client: &http.Client{
			Timeout: ocrRequestTimeout,
			// Provider redirects are not part of the configured OCR contract and
			// must never move the bearer credential to another endpoint.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
	mux := http.NewServeMux()
	mux.HandleFunc(ocrBridgePath, bridge.handle)
	bridge.server = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       ocrRequestTimeout,
		WriteTimeout:      ocrRequestTimeout,
		IdleTimeout:       5 * time.Second,
	}
	go func() {
		// Closing the listener is the normal per-parse shutdown path.
		if serveErr := bridge.server.Serve(listener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			bridge.recordTerminal(&ocrTerminalError{kind: ErrOCRService, message: "OCR bridge stopped unexpectedly."})
		}
	}()
	return bridge, nil
}

func (b *vlmOCRBridge) BaseURL() string {
	return "http://" + b.listener.Addr().String() + "/v1"
}

func (b *vlmOCRBridge) Token() string { return b.token }

func (b *vlmOCRBridge) Close(ctx context.Context) error {
	if b == nil || b.server == nil {
		return nil
	}
	if err := b.server.Shutdown(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("stop OCR bridge: %w", err)
	}
	return nil
}

func (b *vlmOCRBridge) TerminalError() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.terminal
}

func (b *vlmOCRBridge) recordTerminal(err error) {
	if err == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.terminal == nil {
		b.terminal = err
	}
}

func (b *vlmOCRBridge) handle(w http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost || request.URL.Path != ocrBridgePath || request.URL.RawQuery != "" {
		http.NotFound(w, request)
		return
	}
	provided := strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer ")
	if len(provided) != len(b.token) || subtle.ConstantTimeCompare([]byte(provided), []byte(b.token)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, request.Body, ocrBridgeRequestLimit))
	if err != nil {
		b.fail(w, &ocrTerminalError{kind: ErrOCRProtocol, message: "OCR request exceeded the supported size."}, http.StatusRequestEntityTooLarge)
		return
	}
	body, err = rewriteOCRRequest(body, b.config.Model)
	if err != nil {
		b.fail(w, &ocrTerminalError{kind: ErrOCRProtocol, message: "Xberg produced an invalid OCR request."}, http.StatusUnprocessableEntity)
		return
	}
	response, err := b.forwardWithRetry(request.Context(), body)
	if err != nil {
		b.fail(w, err, http.StatusBadGateway)
		return
	}
	defer func() { _ = response.Body.Close() }()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, ocrBridgeResponseLimit+1))
	if err != nil || len(responseBody) > ocrBridgeResponseLimit {
		b.fail(w, &ocrTerminalError{kind: ErrOCRProtocol, message: "OCR provider returned an unreadable or oversized response."}, http.StatusBadGateway)
		return
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		kind := ErrOCRService
		message := "OCR provider rejected the request."
		if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
			kind = ErrOCRConfiguration
			message = "OCR provider credentials were rejected."
		}
		b.fail(w, &ocrTerminalError{kind: kind, message: message}, http.StatusBadGateway)
		return
	}
	sanitized, err := sanitizeOCRResponse(responseBody, b.config.Model)
	if err != nil {
		b.fail(w, err, http.StatusUnprocessableEntity)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(sanitized)
}

func (b *vlmOCRBridge) fail(w http.ResponseWriter, err error, status int) {
	b.recordTerminal(err)
	http.Error(w, "OCR request failed", status)
}

func (b *vlmOCRBridge) forwardWithRetry(ctx context.Context, body []byte) (*http.Response, error) {
	for attempt := range 2 {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, b.config.BaseURL, bytes.NewReader(body))
		if err != nil {
			return nil, &ocrTerminalError{kind: ErrOCRConfiguration, message: "OCR provider endpoint is invalid."}
		}
		request.Header.Set("Authorization", "Bearer "+b.config.APIKey)
		request.Header.Set("Content-Type", "application/json")
		var wroteRequest atomic.Bool
		request = request.WithContext(httptrace.WithClientTrace(request.Context(), &httptrace.ClientTrace{
			WroteRequest: func(httptrace.WroteRequestInfo) {
				// Once the transport has attempted to write the request, the
				// provider outcome is unknown even if Do returns an error. Retrying
				// could repeat a completed and billed inference.
				wroteRequest.Store(true)
			},
		}))
		response, requestErr := b.client.Do(request)
		transient := requestErr != nil && !wroteRequest.Load() ||
			requestErr == nil && (response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= http.StatusInternalServerError)
		if !transient || attempt == 1 {
			if requestErr != nil {
				if errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
					return nil, ctx.Err()
				}
				return nil, &ocrTerminalError{kind: ErrOCRService, message: "OCR provider request failed after one retry."}
			}
			return response, nil
		}
		if response != nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
			_ = response.Body.Close()
		}
		timer := time.NewTimer(ocrRetryDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	panic("unreachable")
}

func rewriteOCRRequest(data []byte, model string) ([]byte, error) {
	var request map[string]any
	if err := decodeSingleJSON(data, &request); err != nil {
		return nil, err
	}
	if request == nil {
		return nil, fmt.Errorf("request must be a JSON object")
	}
	if strings.TrimSpace(model) == "" {
		return nil, fmt.Errorf("model is empty")
	}
	request["model"] = model
	request["stream"] = false
	return json.Marshal(request)
}

func sanitizeOCRResponse(data []byte, model string) ([]byte, error) {
	var response map[string]any
	if err := decodeSingleJSON(data, &response); err != nil {
		return nil, &ocrTerminalError{kind: ErrOCRProtocol, message: "OCR provider returned invalid JSON."}
	}
	choices, ok := response["choices"].([]any)
	if !ok || len(choices) != 1 {
		return nil, &ocrTerminalError{kind: ErrOCRProtocol, message: "OCR provider response must contain exactly one choice."}
	}
	choice, ok := choices[0].(map[string]any)
	if !ok {
		return nil, &ocrTerminalError{kind: ErrOCRProtocol, message: "OCR provider response has an invalid choice."}
	}
	finishReason, ok := choice["finish_reason"].(string)
	if !ok || strings.TrimSpace(finishReason) == "" {
		return nil, &ocrTerminalError{kind: ErrOCRProtocol, message: "OCR provider response has no completion reason."}
	}
	if finishReason == "length" {
		return nil, &ocrTerminalError{
			kind:    ErrOCRProtocol,
			message: "OCR output exceeded the Vision model limit. Configure a Vision model with a larger output limit.",
		}
	}
	if finishReason != "stop" {
		return nil, &ocrTerminalError{kind: ErrOCRProtocol, message: "OCR provider did not complete the transcription normally."}
	}
	message, ok := choice["message"].(map[string]any)
	if !ok {
		return nil, &ocrTerminalError{kind: ErrOCRProtocol, message: "OCR provider response has no message."}
	}
	content, ok := message["content"].(string)
	if !ok {
		return nil, &ocrTerminalError{kind: ErrOCRProtocol, message: "OCR provider response content must be text."}
	}
	for _, field := range []string{"tool_calls", "function_call", "refusal"} {
		if value, exists := message[field]; exists && !emptyJSONValue(value) {
			return nil, &ocrTerminalError{kind: ErrOCRProtocol, message: "OCR provider response contains a conflicting assistant field."}
		}
	}
	var text string
	switch {
	case content == ocrProtocolNoText:
		text = ""
	case strings.HasPrefix(content, ocrProtocolText):
		text = strings.TrimPrefix(content, ocrProtocolText)
		if !hasEffectiveText(text) {
			return nil, &ocrTerminalError{kind: ErrOCRProtocol, message: "OCR TEXT response contains no searchable text."}
		}
	default:
		return nil, &ocrTerminalError{kind: ErrOCRProtocol, message: "OCR provider response did not follow STELLA_OCR_V1."}
	}
	// Return a minimal canonical Chat Completions envelope. Provider-controlled
	// fields that were not explicitly validated never cross into Xberg.
	return json.Marshal(map[string]any{
		"id":      "stella-library-ocr",
		"object":  "chat.completion",
		"created": 0,
		"model":   model,
		"choices": []any{map[string]any{
			"index":         0,
			"finish_reason": "stop",
			"message": map[string]any{
				"role": "assistant", "content": text,
			},
		}},
	})
}

func emptyJSONValue(value any) bool {
	switch value := value.(type) {
	case nil:
		return true
	case string:
		return value == ""
	case []any:
		return len(value) == 0
	case map[string]any:
		return len(value) == 0
	default:
		return false
	}
}

func openAIChatCompletionsURL(baseURL string) (string, error) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil {
		return "", &ocrTerminalError{kind: ErrOCRConfiguration, message: "OCR provider base URL must be an HTTP(S) endpoint."}
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	path := strings.TrimRight(parsed.Path, "/")
	if !strings.HasSuffix(path, "/chat/completions") {
		path += "/chat/completions"
	}
	parsed.Path = path
	return parsed.String(), nil
}
