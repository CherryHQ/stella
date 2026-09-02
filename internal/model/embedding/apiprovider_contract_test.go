package embedding

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"
)

const contractAPIKey = "embedding-contract-token"

type embeddingScript func(t *testing.T, w http.ResponseWriter, r *http.Request)

// scriptedEmbeddingServer rejects requests the test did not describe, making
// accidental retries or fallback traffic visible instead of silently accepted.
type scriptedEmbeddingServer struct {
	t       *testing.T
	server  *httptest.Server
	mu      sync.Mutex
	scripts []embeddingScript
	next    int
}

func newScriptedEmbeddingServer(t *testing.T, scripts ...embeddingScript) *scriptedEmbeddingServer {
	t.Helper()
	s := &scriptedEmbeddingServer{t: t, scripts: scripts}
	s.server = httptest.NewServer(http.HandlerFunc(s.serveHTTP))
	t.Cleanup(s.server.Close)
	t.Cleanup(s.assertConsumed)
	return s
}

func (s *scriptedEmbeddingServer) serveHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	if s.next >= len(s.scripts) {
		s.mu.Unlock()
		s.t.Errorf("embedding API received unscripted %s %s", r.Method, r.URL.Path)
		http.Error(w, "unscripted request", http.StatusInternalServerError)
		return
	}
	script := s.scripts[s.next]
	s.next++
	s.mu.Unlock()
	script(s.t, w, r)
}

func (s *scriptedEmbeddingServer) assertConsumed() {
	s.t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.next != len(s.scripts) {
		s.t.Errorf("embedding API consumed %d of %d scripted requests", s.next, len(s.scripts))
	}
}

func contractProvider(serverURL string) Provider {
	return NewAPIProvider(APIConfig{
		Name:    "contract-api",
		Model:   "contract-model",
		Dim:     3,
		APIKey:  contractAPIKey,
		BaseURL: serverURL + "/v1",
	})
}

func assertEmbeddingRequest(t *testing.T, r *http.Request, wantInput []string) {
	t.Helper()
	if r.Method != http.MethodPost {
		t.Errorf("embedding request method = %s, want POST", r.Method)
	}
	if r.URL.Path != "/v1/embeddings" {
		t.Errorf("embedding request path = %s, want /v1/embeddings", r.URL.Path)
	}
	if got := r.Header.Get("Authorization"); got != "Bearer "+contractAPIKey {
		t.Errorf("embedding request authorization = %q, want fixed Bearer token", got)
	}
	if got := r.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("embedding request content type = %q, want application/json", got)
	}

	var got map[string]any
	if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
		t.Errorf("decode embedding request JSON: %v", err)
		return
	}
	want := map[string]any{
		"input":      stringsToAny(wantInput),
		"model":      "contract-model",
		"dimensions": float64(3),
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("embedding request JSON = %#v, want %#v", got, want)
	}
}

func stringsToAny(values []string) []any {
	result := make([]any, len(values))
	for i, value := range values {
		result[i] = value
	}
	return result
}

func writeEmbeddingResponse(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"object": "list",
		"data":   data,
		"model":  "contract-model",
		"usage": map[string]any{
			"prompt_tokens": 0,
			"total_tokens":  0,
		},
	})
}

func TestAPIProvider_EmbedsContractRequestAndRestoresResponseIndexOrder(t *testing.T) {
	server := newScriptedEmbeddingServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request) {
		assertEmbeddingRequest(t, r, []string{"first", "second"})
		writeEmbeddingResponse(w, []any{
			map[string]any{"object": "embedding", "index": 1, "embedding": []float64{20, 21, 22}},
			map[string]any{"object": "embedding", "index": 0, "embedding": []float64{10, 11, 12}},
		})
	})

	result, err := contractProvider(server.server.URL).Embed(context.Background(), Request{Texts: []string{"first", "second"}})
	if err != nil {
		t.Fatalf("embedding request failed: %v", err)
	}
	want := [][]float32{{10, 11, 12}, {20, 21, 22}}
	if !reflect.DeepEqual(result.Vectors, want) {
		t.Fatalf("vectors = %#v, want input order %#v", result.Vectors, want)
	}
}

func TestAPIProvider_MalformedResponseIsTerminalWithoutVectors(t *testing.T) {
	tests := []struct {
		name string
		data []any
	}{
		{
			name: "duplicate index",
			data: []any{
				map[string]any{"object": "embedding", "index": 0, "embedding": []float64{1, 2, 3}},
				map[string]any{"object": "embedding", "index": 0, "embedding": []float64{4, 5, 6}},
			},
		},
		{
			name: "out of range index",
			data: []any{
				map[string]any{"object": "embedding", "index": 2, "embedding": []float64{1, 2, 3}},
				map[string]any{"object": "embedding", "index": 1, "embedding": []float64{4, 5, 6}},
			},
		},
		{
			name: "missing index",
			data: []any{
				map[string]any{"object": "embedding", "index": 0, "embedding": []float64{1, 2, 3}},
			},
		},
		{
			name: "wrong dimension",
			data: []any{
				map[string]any{"object": "embedding", "index": 0, "embedding": []float64{1, 2}},
				map[string]any{"object": "embedding", "index": 1, "embedding": []float64{4, 5, 6}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newScriptedEmbeddingServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request) {
				assertEmbeddingRequest(t, r, []string{"first", "second"})
				writeEmbeddingResponse(w, tt.data)
			})

			result, err := contractProvider(server.server.URL).Embed(context.Background(), Request{Texts: []string{"first", "second"}})
			if !IsTerminal(err) {
				t.Fatalf("malformed %s error = %v, want terminal", tt.name, err)
			}
			if result.Vectors != nil {
				t.Fatalf("malformed %s returned vectors %#v", tt.name, result.Vectors)
			}
		})
	}
}

func TestChain_CallerCancellationStopsAPIFallbackAndDoesNotTripBreaker(t *testing.T) {
	requestStarted := make(chan struct{}, 1)
	server := newScriptedEmbeddingServer(t,
		func(t *testing.T, w http.ResponseWriter, r *http.Request) {
			assertEmbeddingRequest(t, r, []string{"hi"})
			requestStarted <- struct{}{}
			select {
			case <-r.Context().Done():
			case <-time.After(time.Second):
				t.Errorf("first embedding request was not cancelled within one second")
			}
		},
		func(t *testing.T, w http.ResponseWriter, r *http.Request) {
			assertEmbeddingRequest(t, r, []string{"hi"})
			writeEmbeddingResponse(w, []any{
				map[string]any{"object": "embedding", "index": 0, "embedding": []float64{1, 2, 3}},
			})
		},
	)
	primary := contractProvider(server.server.URL)
	fallback := &fakeProvider{name: "fallback", kind: KindLocal, model: "local-space"}
	chain := NewChain([]Provider{primary, fallback}, BreakerConfig{FailureThreshold: 1, OpenDuration: time.Hour}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type outcome struct {
		result Result
		err    error
	}
	firstResult := make(chan outcome, 1)
	go func() {
		result, err := chain.Embed(ctx, req())
		firstResult <- outcome{result: result, err: err}
	}()

	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("first embedding request did not reach API provider")
	}
	cancel()
	select {
	case got := <-firstResult:
		if !errors.Is(got.err, context.Canceled) {
			t.Fatalf("cancelled chain error = %v, want context.Canceled", got.err)
		}
		if got.result.Vectors != nil {
			t.Fatalf("cancelled chain returned vectors %#v", got.result.Vectors)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled chain did not return within one second")
	}
	if fallback.calls != 0 {
		t.Fatalf("caller cancellation must not call fallback, calls=%d", fallback.calls)
	}

	result, err := chain.Embed(context.Background(), req())
	if err != nil {
		t.Fatalf("second embedding request failed: %v", err)
	}
	if result.ProviderName != "contract-api" {
		t.Fatalf("second request provider = %q, want primary API", result.ProviderName)
	}
	if !reflect.DeepEqual(result.Vectors, [][]float32{{1, 2, 3}}) {
		t.Fatalf("second request vectors = %#v, want primary response", result.Vectors)
	}
	if fallback.calls != 0 {
		t.Fatalf("breaker must not be poisoned by caller cancellation; fallback calls=%d", fallback.calls)
	}
}
