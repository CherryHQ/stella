package live

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	releasecontract "github.com/CherryHQ/stella/test/release"
)

func TestParseProviderLiveConfigRequiresEveryAdvertisedAdapter(t *testing.T) {
	valid := providerConfigJSON("https://gateway.example")
	config, err := parseProviderLiveConfig(valid)
	if err != nil {
		t.Fatal(err)
	}
	if len(config.Targets) != len(advertisedProviderTypes) {
		t.Fatalf("Provider targets = %d, want %d", len(config.Targets), len(advertisedProviderTypes))
	}

	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "credential cannot be smuggled into non-secret JSON",
			raw: strings.Replace(
				valid,
				`"timeout_seconds":30`,
				`"timeout_seconds":30,"api_key":"must-not-live-here"`,
				1,
			),
			want: "unknown field",
		},
		{
			name: "missing advertised adapter",
			raw: strings.Replace(
				valid,
				`,{"id":"responses","type":"openai-response","model":"response-model","base_url":"https://gateway.example/v1"}`,
				"",
				1,
			),
			want: "must cover exactly",
		},
		{
			name: "duplicate target id",
			raw:  strings.Replace(valid, `"id":"chat"`, `"id":"messages"`, 1),
			want: "target id messages is repeated",
		},
		{
			name: "plain HTTP is rejected",
			raw:  strings.Replace(valid, "https://gateway.example", "http://gateway.example", 1),
			want: "absolute HTTPS",
		},
		{
			name: "credential cannot be smuggled into base URL",
			raw: strings.Replace(
				valid,
				"https://gateway.example",
				"https://user:secret@gateway.example",
				1,
			),
			want: "cannot contain credentials",
		},
		{
			name: "trailing document is rejected",
			raw:  valid + `{}`,
			want: "multiple JSON documents",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseProviderLiveConfig(test.raw)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("parse error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestCherryINProviderAdapterRunsExactCandidateForAllAdapters(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "stellad")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	fake := &recordingProviderCandidate{}
	adapter := &cherryINProviderAdapter{
		factory: func(
			_ context.Context,
			gotBinary string,
			run releasecontract.Run,
		) (providerCandidate, error) {
			if gotBinary != binary {
				t.Fatalf("candidate binary = %q, want %q", gotBinary, binary)
			}
			if run.ID != liveRun().ID {
				t.Fatalf("Run ID = %q, want %q", run.ID, liveRun().ID)
			}
			return fake, nil
		},
	}
	inputs := Inputs{
		values: map[string]string{
			EnvCherryINAPIKey:  "unit-test-key",
			EnvProviderTargets: providerConfigJSON("https://gateway.example"),
		},
		run:             liveRun(),
		candidateBinary: binary,
	}
	target := Target{ID: "model-providers", CapabilityID: "X12", ScenarioID: "X12-S02"}

	if err := adapter.Preflight(context.Background(), target, inputs); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Run(context.Background(), target, inputs); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Assert(context.Background(), target, inputs); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Cleanup(context.Background(), target, inputs); err != nil {
		t.Fatal(err)
	}
	if len(fake.targets) != len(advertisedProviderTypes) {
		t.Fatalf("candidate journeys = %d, want %d", len(fake.targets), len(advertisedProviderTypes))
	}
	if !fake.closed {
		t.Fatal("candidate was not closed")
	}
	for i, target := range fake.targets {
		if fake.apiKeys[i] != "unit-test-key" {
			t.Errorf("target %s did not receive the shared credential", target.ID)
		}
		if !strings.Contains(fake.markers[i], liveRun().ID) {
			t.Errorf("target %s marker %q does not contain Run ID", target.ID, fake.markers[i])
		}
		if fake.baseURLs[i] != target.BaseURL {
			t.Errorf("target %s Base URL = %q, want %q", target.ID, fake.baseURLs[i], target.BaseURL)
		}
	}
}

func TestValidateCandidateBinaryRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "stellad")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "candidate-link")
	if err := os.Symlink(binary, link); err != nil {
		t.Skipf("cannot create symlink on this platform: %v", err)
	}
	if err := validateCandidateBinary(link); err == nil {
		t.Fatal("validateCandidateBinary() accepted a symlink")
	}
}

func TestStellaCandidateParsesControlledToolJourney(t *testing.T) {
	const marker = "stella-live-release-1-chat"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/messages") {
			http.Error(w, "unexpected request", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		events := []map[string]any{
			{"type": "tool-input-available", "toolName": "bash", "toolCallId": "call-1"},
			{"type": "tool-output-available", "toolCallId": "call-1", "output": marker},
			{"type": "text-delta", "delta": marker},
		}
		for _, event := range events {
			data, err := json.Marshal(event)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
		}
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	candidate := &stellaCandidate{baseURL: server.URL, client: server.Client()}
	result, err := candidate.streamProviderTurn(
		context.Background(),
		"agent",
		"session",
		"chat",
		marker,
		"controlled prompt",
	)
	if err != nil {
		t.Fatal(err)
	}
	if !result.StreamedText || !result.CalledBash || !result.ObservedOutput ||
		!result.ObservedMarker || !result.CompletedStream {
		t.Fatalf("incomplete journey result: %+v", result)
	}
}

func TestStellaCandidateTreatsLocalSSETimeoutAsProductFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		// Keep the local candidate stream open until the runner deadline. This
		// reproduces the boundary exposed by the OpenAI SSE deadlock in #812.
		<-r.Context().Done()
	}))
	defer server.Close()

	candidate := &stellaCandidate{baseURL: server.URL, client: server.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := candidate.streamProviderTurn(
		ctx,
		"agent",
		"session",
		"chat",
		"marker",
		"controlled prompt",
	)
	if err == nil {
		t.Fatal("streamProviderTurn() error = nil, want local SSE timeout")
	}
	var external *providerExternalError
	if errors.As(err, &external) {
		t.Fatalf("local candidate SSE timeout was classified as external: %v", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("streamProviderTurn() error = %v, want context deadline", err)
	}
}

func TestCherryINProviderAdapterClassifiesCandidateFailureAsProductFailure(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "stellad")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	fake := &recordingProviderCandidate{
		runErr: fmt.Errorf("read candidate Session SSE: %w", context.DeadlineExceeded),
	}
	adapter := &cherryINProviderAdapter{
		factory: func(context.Context, string, releasecontract.Run) (providerCandidate, error) {
			return fake, nil
		},
	}
	inputs := Inputs{
		values: map[string]string{
			EnvCherryINAPIKey:  "unit-test-key",
			EnvProviderTargets: providerConfigJSON("https://gateway.example"),
		},
		run:             liveRun(),
		candidateBinary: binary,
	}
	target := Target{ID: "model-providers", CapabilityID: "X12", ScenarioID: "X12-S02"}

	if err := adapter.Preflight(context.Background(), target, inputs); err != nil {
		t.Fatal(err)
	}
	err := adapter.Run(context.Background(), target, inputs)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Status != releasecontract.StatusProductFailure {
		t.Fatalf("adapter Run() error = %v, want Product Failure", err)
	}
	if err := adapter.Cleanup(context.Background(), target, inputs); err != nil {
		t.Fatal(err)
	}
}

func TestCherryINEmbeddingAdapterUsesProductionWireContractAndRanksFixture(t *testing.T) {
	const apiKey = "unit-test-embedding-key"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/embeddings" {
			http.Error(w, "unexpected endpoint", http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+apiKey {
			http.Error(w, "missing test credential", http.StatusUnauthorized)
			return
		}
		var request struct {
			Input      []string `json:"input"`
			Model      string   `json:"model"`
			Dimensions int      `json:"dimensions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if request.Model != "embedding-model" || request.Dimensions != 3 {
			http.Error(w, "unexpected model configuration", http.StatusBadRequest)
			return
		}

		data := make([]map[string]any, len(request.Input))
		for i, text := range request.Input {
			vector := []float64{0, 1, 0}
			switch {
			case strings.Contains(text, "Saturn"), strings.Contains(text, "planet surrounded by rings"):
				vector = []float64{1, 0, 0}
			case strings.Contains(text, "Sourdough"):
				vector = []float64{0, 0, 1}
			}
			data[i] = map[string]any{
				"object":    "embedding",
				"embedding": vector,
				"index":     i,
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"object": "list",
			"data":   data,
			"model":  request.Model,
			"usage":  map[string]int{"prompt_tokens": 4, "total_tokens": 4},
		})
	}))
	defer server.Close()

	// The production embedding provider intentionally uses http.DefaultTransport.
	// Swap only for this serial test so the local TLS certificate is trusted.
	originalTransport := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	defer func() { http.DefaultTransport = originalTransport }()

	adapter := &cherryINEmbeddingAdapter{}
	inputs := Inputs{values: map[string]string{
		EnvCherryINAPIKey: apiKey,
		EnvEmbeddingTarget: fmt.Sprintf(
			`{"base_url":%q,"model":"embedding-model","dimensions":3,"timeout_seconds":30}`,
			server.URL+"/v1",
		),
	}}
	target := Target{ID: "embedding-provider", CapabilityID: "X14", ScenarioID: "X14-S02"}
	if err := adapter.Preflight(context.Background(), target, inputs); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Run(context.Background(), target, inputs); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Assert(context.Background(), target, inputs); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Cleanup(context.Background(), target, inputs); err != nil {
		t.Fatal(err)
	}
}

type recordingProviderCandidate struct {
	targets  []providerTargetConfig
	apiKeys  []string
	markers  []string
	baseURLs []string
	closed   bool
	runErr   error
}

func (c *recordingProviderCandidate) RunProviderJourney(
	_ context.Context,
	target providerTargetConfig,
	baseURL string,
	apiKey string,
	marker string,
) (providerJourneyResult, error) {
	c.targets = append(c.targets, target)
	c.apiKeys = append(c.apiKeys, apiKey)
	c.markers = append(c.markers, marker)
	c.baseURLs = append(c.baseURLs, baseURL)
	if c.runErr != nil {
		return providerJourneyResult{}, c.runErr
	}
	return providerJourneyResult{
		TargetID:        target.ID,
		StreamedText:    true,
		CalledBash:      true,
		ObservedOutput:  true,
		ObservedMarker:  true,
		CompletedStream: true,
	}, nil
}

func (c *recordingProviderCandidate) Close(context.Context) error {
	c.closed = true
	return nil
}

func providerConfigJSON(gatewayRoot string) string {
	return fmt.Sprintf(
		`{"timeout_seconds":30,"targets":[`+
			`{"id":"messages","type":"anthropic","model":"messages-model","base_url":%q},`+
			`{"id":"chat","type":"openai","model":"chat-model","base_url":%q},`+
			`{"id":"responses","type":"openai-response","model":"response-model","base_url":%q}]}`,
		gatewayRoot,
		gatewayRoot+"/v1",
		gatewayRoot+"/v1",
	)
}
