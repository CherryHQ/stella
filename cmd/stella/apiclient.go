package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	apiclient "github.com/CherryHQ/stella/api/client"
	apitypes "github.com/CherryHQ/stella/api/types"

	"github.com/CherryHQ/stella/internal/config"
)

func newAPIClient(extra ...apiclient.ClientOption) (*apiclient.Client, error) {
	token := os.Getenv("STELLA_TOKEN")
	if token == "" {
		return nil, fmt.Errorf("STELLA_TOKEN env var is required (run 'stella serve' and set STELLA_TOKEN to a generated token)")
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

// readBody reads the response body and checks for HTTP errors.
// On 4xx/5xx it returns a formatted error; otherwise it returns the raw bytes.
func readBody(resp *http.Response) ([]byte, error) {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode >= 400 {
		var apiErr apitypes.Error
		if jerr := json.Unmarshal(body, &apiErr); jerr == nil && apiErr.Error != "" {
			return nil, fmt.Errorf("stella server %d: %s", resp.StatusCode, apiErr.Error)
		}
		snippet := strings.TrimSpace(string(body))
		if len(snippet) > 200 {
			snippet = snippet[:200] + "…"
		}
		if snippet == "" {
			return nil, fmt.Errorf("stella server returned %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("stella server %d: %s", resp.StatusCode, snippet)
	}
	return body, nil
}

// decodeJSON reads the response body and unmarshals it directly into out.
// Used by endpoints that return bare JSON (e.g. scheduler, recally).
func decodeJSON(resp *http.Response, out any) error {
	body, err := readBody(resp)
	if err != nil {
		return err
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	return json.Unmarshal(body, out)
}

// decodeDataJSON reads the response body and unmarshals the "data" field
// from the {"data": ...} envelope used by most admin/profile API endpoints.
func decodeDataJSON(resp *http.Response, out any) error {
	body, err := readBody(resp)
	if err != nil {
		return err
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

// apiCall creates an API client, executes the given call, closes the response
// body, and decodes the {"data": ...} envelope into out.
func apiCall[T any](call func(*apiclient.Client) (*http.Response, error)) (T, error) {
	var zero T
	api, err := newAPIClient()
	if err != nil {
		return zero, err
	}
	resp, err := call(api)
	if err != nil {
		return zero, wrapServerErr(err)
	}
	defer resp.Body.Close() //nolint:errcheck
	var out T
	if err := decodeDataJSON(resp, &out); err != nil {
		return zero, err
	}
	return out, nil
}

// apiDo is like apiCall but for endpoints where no response body is needed.
func apiDo(call func(*apiclient.Client) (*http.Response, error)) error {
	api, err := newAPIClient()
	if err != nil {
		return err
	}
	resp, err := call(api)
	if err != nil {
		return wrapServerErr(err)
	}
	defer resp.Body.Close() //nolint:errcheck
	return decodeDataJSON(resp, nil)
}

func ptr[T any](v T) *T { return &v }
