package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	apiclient "github.com/vaayne/anna/api/client"
	apitypes "github.com/vaayne/anna/api/types"

	"github.com/vaayne/anna/internal/config"
)

func newAPIClient(extra ...apiclient.ClientOption) (*apiclient.Client, error) {
	token := os.Getenv("ANNA_TOKEN")
	if token == "" {
		return nil, fmt.Errorf("ANNA_TOKEN env var is required (run 'anna serve' and set ANNA_TOKEN to a generated token)")
	}
	opts := append([]apiclient.ClientOption{apiclient.WithRequestEditorFn(bearerAuth(token))}, extra...)
	return apiclient.NewClient(config.ServerURL(), opts...)
}

func bearerAuth(token string) apiclient.RequestEditorFn {
	return func(_ context.Context, req *http.Request) error {
		req.Header.Set("Authorization", "Bearer "+token)
		return nil
	}
}

func decodeJSON(resp *http.Response, out any) error {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode >= 400 {
		var apiErr apitypes.Error
		if jerr := json.Unmarshal(body, &apiErr); jerr == nil && apiErr.Error != "" {
			return fmt.Errorf("anna server %d: %s", resp.StatusCode, apiErr.Error)
		}
		snippet := strings.TrimSpace(string(body))
		if len(snippet) > 200 {
			snippet = snippet[:200] + "…"
		}
		if snippet == "" {
			return fmt.Errorf("anna server returned %d", resp.StatusCode)
		}
		return fmt.Errorf("anna server %d: %s", resp.StatusCode, snippet)
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

func decodeData(resp *http.Response, out any) error {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode >= 400 {
		var apiErr apitypes.Error
		if jerr := json.Unmarshal(body, &apiErr); jerr == nil && apiErr.Error != "" {
			return fmt.Errorf("anna server %d: %s", resp.StatusCode, apiErr.Error)
		}
		snippet := strings.TrimSpace(string(body))
		if len(snippet) > 200 {
			snippet = snippet[:200] + "…"
		}
		if snippet == "" {
			return fmt.Errorf("anna server returned %d", resp.StatusCode)
		}
		return fmt.Errorf("anna server %d: %s", resp.StatusCode, snippet)
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return json.Unmarshal(envelope.Data, out)
}

func ptr[T any](v T) *T { return &v }
